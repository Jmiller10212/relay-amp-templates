package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"relay/internal/model"
)

var ErrUsernameTaken = errors.New("username is already taken")
var ErrCooldown = errors.New("username change cooldown is active")
var ErrNoReservation = errors.New("registration reservation was not found")
var ErrRecoveryGrant = errors.New("recovery grant is missing, expired, or already used")
var ErrFriendRequestNotFound = errors.New("friend request was not found")
var ErrAlreadyFriends = errors.New("users are already friends")
var ErrConversationNotFound = errors.New("conversation was not found")
var ErrFriendshipRequired = errors.New("active friendship is required")
var ErrInvalidReadCursor = errors.New("read cursor does not belong to the conversation")
var ErrServerNotFound = errors.New("server was not found")
var ErrServerLimit = errors.New("server limit reached")
var ErrMembershipLimit = errors.New("membership limit reached")
var ErrOwnerRequired = errors.New("server owner permission required")
var ErrOwnerCannotLeave = errors.New("owner cannot leave server")
var ErrInviteExists = errors.New("server invitation already exists")
var ErrInviteNotFound = errors.New("server invitation was not found")
var ErrAlreadyMember = errors.New("user is already a server member")
var ErrInviteRateLimit = errors.New("server invitation rate limit reached")
var ErrConfirmation = errors.New("confirmation did not match")

type PendingRegistration struct {
	Nonce, EmailHash, AuthUserID, Username, DisplayName string
	ExpiresAt                                           time.Time
}

type Module struct {
	dataDir           string
	maxStoredMessages int
	db                *sql.DB
}

func New(dataDir string, maxStoredMessages ...int) *Module {
	limit := 0
	if len(maxStoredMessages) > 0 {
		limit = maxStoredMessages[0]
	}
	return &Module{dataDir: dataDir, maxStoredMessages: limit}
}
func (m *Module) Name() string                      { return "persistence" }
func (m *Module) Dependencies() []string            { return nil }
func (m *Module) RegisterHTTP(*http.ServeMux)       {}
func (m *Module) RegisterWebSockets(*http.ServeMux) {}
func (m *Module) Start(context.Context) error       { return nil }

func (m *Module) Init(ctx context.Context) error {
	if err := os.MkdirAll(m.dataDir, 0o750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	dbPath := filepath.Join(m.dataDir, "relay.db")
	_, statErr := os.Stat(dbPath)
	existed := statErr == nil
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	m.db = db
	for _, stmt := range []string{`PRAGMA journal_mode=WAL;`, `PRAGMA busy_timeout=5000;`, `PRAGMA foreign_keys=ON;`, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);`} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			_ = db.Close()
			m.db = nil
			return fmt.Errorf("database setup: %w", err)
		}
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	const currentSchemaVersion = 7
	if version >= 1 && version < currentSchemaVersion && existed {
		if err := m.backup(ctx, dbPath, version+1); err != nil {
			return fmt.Errorf("pre-migration backup: %w", err)
		}
	}
	migrations := []struct {
		version    int
		statements []string
	}{
		{1, []string{`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			kind TEXT NOT NULL CHECK(kind IN ('user','system')),
			username TEXT NOT NULL,
			text TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`}},
		{2, []string{
			`CREATE TABLE relay_users (auth_user_id TEXT PRIMARY KEY, username TEXT NOT NULL COLLATE NOCASE, display_name TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, username_changed_at TEXT);`,
			`CREATE UNIQUE INDEX relay_users_username_nocase ON relay_users(username COLLATE NOCASE);`,
			`CREATE TABLE pending_registrations (nonce TEXT PRIMARY KEY, email_hash TEXT NOT NULL, auth_user_id TEXT, username TEXT NOT NULL COLLATE NOCASE, display_name TEXT NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL);`,
			`CREATE UNIQUE INDEX pending_username_nocase ON pending_registrations(username COLLATE NOCASE);`,
			`CREATE INDEX pending_email_hash ON pending_registrations(email_hash);`,
			`CREATE TABLE recovery_grants (session_id TEXT PRIMARY KEY, auth_user_id TEXT NOT NULL, expires_at TEXT NOT NULL, used_at TEXT);`,
			`ALTER TABLE messages ADD COLUMN user_id TEXT REFERENCES relay_users(auth_user_id);`,
			`CREATE INDEX messages_user_id ON messages(user_id);`,
		}},
		// Versions through 0.3.5 could retain a username reservation after
		// Supabase returned its duplicate-email decoy response. Clearing
		// unconsumed registrations repairs those stale locks. A genuinely new
		// user can still finish their profile after authentication.
		{3, []string{`DELETE FROM pending_registrations;`}},
		{4, []string{
			`CREATE TABLE conversations (id TEXT PRIMARY KEY, kind TEXT NOT NULL CHECK(kind IN ('global','direct','channel')), created_at TEXT NOT NULL);`,
			`INSERT INTO conversations(id,kind,created_at) VALUES('00000000-0000-7000-8000-000000000001','global',strftime('%Y-%m-%dT%H:%M:%fZ','now'));`,
			`ALTER TABLE messages ADD COLUMN conversation_id TEXT REFERENCES conversations(id);`,
			`UPDATE messages SET conversation_id='00000000-0000-7000-8000-000000000001' WHERE conversation_id IS NULL;`,
			`CREATE INDEX messages_conversation_id ON messages(conversation_id,id);`,
		}},
		{5, []string{
			`CREATE TABLE friend_requests (
				id TEXT PRIMARY KEY,
				requester_id TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				recipient_id TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				user_low TEXT NOT NULL,
				user_high TEXT NOT NULL,
				created_at TEXT NOT NULL,
				CHECK(requester_id <> recipient_id),
				CHECK(user_low < user_high),
				UNIQUE(user_low,user_high)
			);`,
			`CREATE INDEX friend_requests_recipient ON friend_requests(recipient_id,created_at);`,
			`CREATE TABLE friendships (
				user_low TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				user_high TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				created_at TEXT NOT NULL,
				CHECK(user_low < user_high),
				PRIMARY KEY(user_low,user_high)
			);`,
			`CREATE INDEX friendships_high ON friendships(user_high);`,
		}},
		{6, []string{
			`CREATE TABLE direct_conversations (
				conversation_id TEXT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
				user_low TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				user_high TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				CHECK(user_low < user_high),
				UNIQUE(user_low,user_high)
			);`,
			`CREATE INDEX direct_conversations_low ON direct_conversations(user_low);`,
			`CREATE INDEX direct_conversations_high ON direct_conversations(user_high);`,
			`CREATE TABLE conversation_reads (
				conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
				user_id TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				last_read_message_id INTEGER NOT NULL DEFAULT 0,
				updated_at TEXT NOT NULL,
				PRIMARY KEY(conversation_id,user_id)
			);`,
			`CREATE INDEX conversation_reads_user ON conversation_reads(user_id,conversation_id);`,
		}},
		{7, []string{
			`CREATE TABLE servers (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				owner_user_id TEXT NOT NULL REFERENCES relay_users(auth_user_id),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			);`,
			`CREATE INDEX servers_owner ON servers(owner_user_id);`,
			`CREATE TABLE server_members (
				server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
				user_id TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				joined_at TEXT NOT NULL,
				PRIMARY KEY(server_id,user_id)
			);`,
			`CREATE INDEX server_members_user ON server_members(user_id,server_id);`,
			`CREATE TABLE server_invites (
				id TEXT PRIMARY KEY,
				server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
				inviter_id TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				invitee_id TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				created_at TEXT NOT NULL,
				CHECK(inviter_id <> invitee_id),
				UNIQUE(server_id,invitee_id)
			);`,
			`CREATE INDEX server_invites_invitee ON server_invites(invitee_id,created_at);`,
			`CREATE INDEX server_invites_inviter ON server_invites(inviter_id,created_at);`,
			`CREATE TABLE server_invite_log (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				inviter_id TEXT NOT NULL REFERENCES relay_users(auth_user_id) ON DELETE CASCADE,
				created_at TEXT NOT NULL
			);`,
			`CREATE INDEX server_invite_log_inviter ON server_invite_log(inviter_id,created_at);`,
			`CREATE TABLE server_channels (
				id TEXT PRIMARY KEY,
				server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
				conversation_id TEXT NOT NULL UNIQUE REFERENCES conversations(id),
				name TEXT NOT NULL COLLATE NOCASE,
				position INTEGER NOT NULL DEFAULT 0,
				creator_id TEXT NOT NULL REFERENCES relay_users(auth_user_id),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				UNIQUE(server_id,name COLLATE NOCASE)
			);`,
			`CREATE INDEX server_channels_server ON server_channels(server_id,position,id);`,
		}},
	}
	for _, migration := range migrations {
		if migration.version <= version {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range migration.statements {
			if _, err = tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", migration.version, err)
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES(?)`, migration.version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	_, err = db.ExecContext(ctx, `DELETE FROM pending_registrations WHERE expires_at <= ?; DELETE FROM recovery_grants WHERE expires_at <= ? OR used_at IS NOT NULL;`, nowText(), nowText())
	return err
}

func (m *Module) backup(ctx context.Context, dbPath string, targetVersion int) error {
	if _, err := m.db.ExecContext(ctx, `PRAGMA wal_checkpoint(FULL);`); err != nil {
		return err
	}
	src, err := os.Open(dbPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dstPath := filepath.Join(m.dataDir, fmt.Sprintf("relay.db.pre-v%d-%s.backup", targetVersion, time.Now().UTC().Format("20060102T150405Z")))
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (m *Module) Stop(context.Context) error {
	if m.db == nil {
		return nil
	}
	return m.db.Close()
}

func (m *Module) Insert(ctx context.Context, kind, userID, username, displayName, text string) (model.Message, error) {
	return m.InsertConversation(ctx, model.GlobalConversationID, kind, userID, username, displayName, text)
}

func (m *Module) InsertConversation(ctx context.Context, conversationID, kind, userID, username, displayName, text string) (model.Message, error) {
	if m.db == nil {
		return model.Message{}, errors.New("database is not initialized")
	}
	created := time.Now().UTC().Truncate(time.Millisecond)
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Message{}, err
	}
	defer tx.Rollback()
	var uid any
	if userID != "" {
		uid = userID
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO messages(conversation_id,kind,user_id,username,text,created_at) VALUES(?,?,?,?,?,?)`, conversationID, kind, uid, username, text, created.Format(time.RFC3339Nano))
	if err != nil {
		return model.Message{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.Message{}, err
	}
	if m.maxStoredMessages > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id NOT IN (SELECT id FROM messages ORDER BY id DESC LIMIT ?)`, m.maxStoredMessages); err != nil {
			return model.Message{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Message{}, err
	}
	if displayName == "" {
		displayName = username
	}
	return model.Message{ID: id, ConversationID: conversationID, Kind: kind, UserID: userID, Username: username, DisplayName: displayName, Text: text, CreatedAt: created}, nil
}

func (m *Module) Recent(ctx context.Context, limit int) ([]model.Message, error) {
	return m.RecentConversation(ctx, model.GlobalConversationID, 0, limit)
}

func (m *Module) RecentConversation(ctx context.Context, conversationID string, beforeID int64, limit int) ([]model.Message, error) {
	if m.db == nil {
		return nil, errors.New("database is not initialized")
	}
	query := `SELECT m.id,COALESCE(m.conversation_id,''),m.kind,COALESCE(m.user_id,''),m.username,COALESCE(u.display_name,m.username),m.text,m.created_at FROM messages m LEFT JOIN relay_users u ON u.auth_user_id=m.user_id WHERE m.conversation_id=?`
	args := []any{conversationID}
	if beforeID > 0 {
		query += ` AND m.id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY m.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rev := make([]model.Message, 0, limit)
	for rows.Next() {
		var msg model.Message
		var raw string
		if err := rows.Scan(&msg.ID, &msg.ConversationID, &msg.Kind, &msg.UserID, &msg.Username, &msg.DisplayName, &msg.Text, &raw); err != nil {
			return nil, err
		}
		msg.CreatedAt, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, err
		}
		rev = append(rev, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]model.Message, len(rev))
	for i := range rev {
		out[len(rev)-1-i] = rev[i]
	}
	return out, nil
}

func (m *Module) Conversation(ctx context.Context, id string) (model.Conversation, error) {
	var conversation model.Conversation
	var created string
	err := m.db.QueryRowContext(ctx, `SELECT id,kind,created_at FROM conversations WHERE id=?`, id).Scan(&conversation.ID, &conversation.Kind, &created)
	if err != nil {
		return model.Conversation{}, err
	}
	conversation.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		conversation.CreatedAt, err = time.Parse("2006-01-02T15:04:05.000Z", created)
	}
	return conversation, err
}

func (m *Module) ReserveRegistration(ctx context.Context, p PendingRegistration) error {
	_, _ = m.db.ExecContext(ctx, `DELETE FROM pending_registrations WHERE expires_at <= ?`, nowText())
	_, err := m.db.ExecContext(ctx, `INSERT INTO pending_registrations(nonce,email_hash,auth_user_id,username,display_name,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`, p.Nonce, p.EmailHash, nullString(p.AuthUserID), p.Username, p.DisplayName, nowText(), p.ExpiresAt.UTC().Format(time.RFC3339Nano))
	if isUnique(err) {
		return ErrUsernameTaken
	}
	return err
}
func (m *Module) ReleaseRegistration(ctx context.Context, nonce string) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM pending_registrations WHERE nonce=?`, nonce)
	return err
}
func (m *Module) AttachReservationUser(ctx context.Context, nonce, authUserID string) error {
	_, err := m.db.ExecContext(ctx, `UPDATE pending_registrations SET auth_user_id=? WHERE nonce=?`, authUserID, nonce)
	return err
}

func (m *Module) ProvisionReservedProfile(ctx context.Context, authUserID, emailHash string) (model.Profile, error) {
	if p, err := m.Profile(ctx, authUserID); err == nil {
		return p, nil
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Profile{}, err
	}
	defer tx.Rollback()
	var nonce, username, displayName string
	err = tx.QueryRowContext(ctx, `SELECT nonce,username,display_name FROM pending_registrations WHERE email_hash=? AND expires_at>? AND (auth_user_id IS NULL OR auth_user_id=?) ORDER BY created_at DESC LIMIT 1`, emailHash, nowText(), authUserID).Scan(&nonce, &username, &displayName)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Profile{}, ErrNoReservation
	}
	if err != nil {
		return model.Profile{}, err
	}
	now := nowText()
	_, err = tx.ExecContext(ctx, `INSERT INTO relay_users(auth_user_id,username,display_name,created_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(auth_user_id) DO NOTHING`, authUserID, username, displayName, now, now)
	if isUnique(err) {
		return model.Profile{}, ErrUsernameTaken
	}
	if err != nil {
		return model.Profile{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM pending_registrations WHERE nonce=?`, nonce); err != nil {
		return model.Profile{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Profile{}, err
	}
	return m.Profile(ctx, authUserID)
}

func (m *Module) CreateProfile(ctx context.Context, authUserID, username, displayName string) (model.Profile, error) {
	now := nowText()
	_, err := m.db.ExecContext(ctx, `INSERT INTO relay_users(auth_user_id,username,display_name,created_at,updated_at) VALUES(?,?,?,?,?)`, authUserID, username, displayName, now, now)
	if isUnique(err) {
		return model.Profile{}, ErrUsernameTaken
	}
	if err != nil {
		return model.Profile{}, err
	}
	return m.Profile(ctx, authUserID)
}
func (m *Module) Profile(ctx context.Context, authUserID string) (model.Profile, error) {
	var p model.Profile
	var created, updated string
	var changed sql.NullString
	err := m.db.QueryRowContext(ctx, `SELECT auth_user_id,username,display_name,created_at,updated_at,username_changed_at FROM relay_users WHERE auth_user_id=?`, authUserID).Scan(&p.ID, &p.Username, &p.DisplayName, &created, &updated, &changed)
	if err != nil {
		return model.Profile{}, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if changed.Valid {
		t, _ := time.Parse(time.RFC3339Nano, changed.String)
		p.UsernameChangedAt = &t
	}
	return p, nil
}
func (m *Module) UpdateDisplayName(ctx context.Context, authUserID, displayName string) (model.Profile, error) {
	res, err := m.db.ExecContext(ctx, `UPDATE relay_users SET display_name=?,updated_at=? WHERE auth_user_id=?`, displayName, nowText(), authUserID)
	if err != nil {
		return model.Profile{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return model.Profile{}, sql.ErrNoRows
	}
	return m.Profile(ctx, authUserID)
}
func (m *Module) UpdateUsername(ctx context.Context, authUserID, username string, cooldown time.Duration) (model.Profile, error) {
	p, err := m.Profile(ctx, authUserID)
	if err != nil {
		return model.Profile{}, err
	}
	if p.UsernameChangedAt != nil && time.Since(*p.UsernameChangedAt) < cooldown {
		return model.Profile{}, ErrCooldown
	}
	now := nowText()
	_, err = m.db.ExecContext(ctx, `UPDATE relay_users SET username=?,username_changed_at=?,updated_at=? WHERE auth_user_id=?`, username, now, now, authUserID)
	if isUnique(err) {
		return model.Profile{}, ErrUsernameTaken
	}
	if err != nil {
		return model.Profile{}, err
	}
	return m.Profile(ctx, authUserID)
}

func (m *Module) CreateRecoveryGrant(ctx context.Context, sessionID, authUserID string, expires time.Time) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO recovery_grants(session_id,auth_user_id,expires_at,used_at) VALUES(?,?,?,NULL) ON CONFLICT(session_id) DO UPDATE SET auth_user_id=excluded.auth_user_id,expires_at=excluded.expires_at,used_at=NULL`, sessionID, authUserID, expires.UTC().Format(time.RFC3339Nano))
	return err
}
func (m *Module) ConsumeRecoveryGrant(ctx context.Context, sessionID, authUserID string) error {
	res, err := m.db.ExecContext(ctx, `UPDATE recovery_grants SET used_at=? WHERE session_id=? AND auth_user_id=? AND expires_at>? AND used_at IS NULL`, nowText(), sessionID, authUserID, nowText())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrRecoveryGrant
	}
	return nil
}

func (m *Module) Healthy(ctx context.Context) bool {
	return m.db != nil && m.db.PingContext(ctx) == nil
}
func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func isUnique(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
