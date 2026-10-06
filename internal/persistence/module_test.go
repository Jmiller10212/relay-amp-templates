package persistence

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"relay/internal/model"
)

func TestPersistenceAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := New(dir)
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	one, err := m.Insert(ctx, "user", "", "Ada", "Ada", "hello")
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Stop(ctx)
	m = New(dir)
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	got, err := m.Recent(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != one.ID || got[0].Text != "hello" {
		t.Fatalf("unexpected messages: %+v", got)
	}
}

func TestLegacyMigrationCreatesBackupAndPreservesMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relay.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)`, `INSERT INTO schema_migrations VALUES(1)`, `CREATE TABLE messages(id INTEGER PRIMARY KEY AUTOINCREMENT,kind TEXT NOT NULL,username TEXT NOT NULL,text TEXT NOT NULL,created_at TEXT NOT NULL)`, `INSERT INTO messages(kind,username,text,created_at) VALUES('user','Legacy','hello','2026-01-01T00:00:00Z')`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	m := New(dir)
	if err := m.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())
	got, err := m.Recent(context.Background(), 10)
	if err != nil || len(got) != 1 || got[0].UserID != "" || got[0].Username != "Legacy" {
		t.Fatalf("messages=%+v err=%v", got, err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "relay.db.pre-v*-*.backup"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Size() == 0 {
		t.Fatalf("invalid backup: %v", err)
	}
}

func TestReservationCooldownAndRecoveryGrant(t *testing.T) {
	ctx := context.Background()
	m := New(t.TempDir())
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	p := PendingRegistration{Nonce: "one", EmailHash: "hash", Username: "person", DisplayName: "Person", ExpiresAt: time.Now().Add(time.Hour)}
	if err := m.ReserveRegistration(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Nonce = "two"
	p.EmailHash = "other"
	if err := m.ReserveRegistration(ctx, p); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate err=%v", err)
	}
	profile, err := m.ProvisionReservedProfile(ctx, "auth-1", "hash")
	if err != nil || profile.Username != "person" {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	if _, err := m.UpdateUsername(ctx, "auth-1", "person2", 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateUsername(ctx, "auth-1", "person3", 7*24*time.Hour); !errors.Is(err, ErrCooldown) {
		t.Fatalf("cooldown err=%v", err)
	}
	if err := m.CreateRecoveryGrant(ctx, "session", "auth-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := m.ConsumeRecoveryGrant(ctx, "session", "auth-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.ConsumeRecoveryGrant(ctx, "session", "auth-1"); !errors.Is(err, ErrRecoveryGrant) {
		t.Fatalf("replay err=%v", err)
	}
}

func TestReleaseRegistrationMakesUsernameAvailable(t *testing.T) {
	ctx := context.Background()
	m := New(t.TempDir())
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	p := PendingRegistration{Nonce: "one", EmailHash: "hash", Username: "available", DisplayName: "Person", ExpiresAt: time.Now().Add(time.Hour)}
	if err := m.ReserveRegistration(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseRegistration(ctx, p.Nonce); err != nil {
		t.Fatal(err)
	}
	p.Nonce = "two"
	p.EmailHash = "other"
	if err := m.ReserveRegistration(ctx, p); err != nil {
		t.Fatalf("released username remained unavailable: %v", err)
	}
}

func TestV2MigrationClearsStaleReservationsAndCreatesBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := New(dir)
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.ReserveRegistration(ctx, PendingRegistration{Nonce: "stale", EmailHash: "hash", AuthUserID: "decoy-id", Username: "stuck", DisplayName: "Stuck", ExpiresAt: time.Now().Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.ExecContext(ctx, `DROP INDEX server_channels_server; DROP TABLE server_channels; DROP INDEX server_invite_log_inviter; DROP TABLE server_invite_log; DROP INDEX server_invites_inviter; DROP INDEX server_invites_invitee; DROP TABLE server_invites; DROP INDEX server_members_user; DROP TABLE server_members; DROP INDEX servers_owner; DROP TABLE servers; DROP INDEX conversation_reads_user; DROP TABLE conversation_reads; DROP INDEX direct_conversations_high; DROP INDEX direct_conversations_low; DROP TABLE direct_conversations; DROP INDEX friendships_high; DROP TABLE friendships; DROP INDEX friend_requests_recipient; DROP TABLE friend_requests; DROP INDEX messages_conversation_id; ALTER TABLE messages DROP COLUMN conversation_id; DROP TABLE conversations; DELETE FROM schema_migrations WHERE version>=3`); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	m = New(dir)
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	if err := m.ReserveRegistration(ctx, PendingRegistration{Nonce: "fresh", EmailHash: "fresh-hash", Username: "stuck", DisplayName: "Fresh", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("stale username was not cleared: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "relay.db.pre-v*-*.backup"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
}

func TestRetentionLimitKeepsNewestMessages(t *testing.T) {
	ctx := context.Background()
	m := New(t.TempDir(), 2)
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	for _, text := range []string{"one", "two", "three"} {
		if _, err := m.Insert(ctx, "user", "", "Ada", "Ada", text); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "two" || got[1].Text != "three" {
		t.Fatalf("unexpected retained messages: %+v", got)
	}
}

func TestMigrationFourBackfillsLegacyMessagesIntoGlobalLobby(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY);`,
		`INSERT INTO schema_migrations(version) VALUES(1),(2),(3);`,
		`CREATE TABLE relay_users (auth_user_id TEXT PRIMARY KEY, username TEXT NOT NULL COLLATE NOCASE, display_name TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, username_changed_at TEXT);`,
		`CREATE TABLE pending_registrations (nonce TEXT PRIMARY KEY, email_hash TEXT NOT NULL, auth_user_id TEXT, username TEXT NOT NULL COLLATE NOCASE, display_name TEXT NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL);`,
		`CREATE TABLE recovery_grants (session_id TEXT PRIMARY KEY, auth_user_id TEXT NOT NULL, expires_at TEXT NOT NULL, used_at TEXT);`,
		`CREATE TABLE messages(id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, username TEXT NOT NULL, text TEXT NOT NULL, created_at TEXT NOT NULL, user_id TEXT REFERENCES relay_users(auth_user_id));`,
		`INSERT INTO messages(kind,username,text,created_at) VALUES('user','Legacy','kept','2026-01-01T00:00:00Z');`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	messages, err := m.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != 1 || messages[0].Text != "kept" || messages[0].ConversationID != model.GlobalConversationID {
		t.Fatalf("legacy message changed: %+v", messages)
	}
}
