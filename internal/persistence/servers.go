package persistence

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"relay/internal/model"
)

func (m *Module) CreateServer(ctx context.Context, serverID, conversationID, channelID, ownerID, name string, maxOwned, maxMemberships int) (model.Server, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Server{}, err
	}
	defer tx.Rollback()
	var owned, memberships int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE owner_user_id=?`, ownerID).Scan(&owned); err != nil {
		return model.Server{}, err
	}
	if owned >= maxOwned {
		return model.Server{}, ErrServerLimit
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_members WHERE user_id=?`, ownerID).Scan(&memberships); err != nil {
		return model.Server{}, err
	}
	if memberships >= maxMemberships {
		return model.Server{}, ErrMembershipLimit
	}
	if err = ensureServerNameAvailable(ctx, tx, ownerID, name, ""); err != nil {
		return model.Server{}, err
	}
	now := nowText()
	if _, err = tx.ExecContext(ctx, `INSERT INTO servers(id,name,name_key,owner_user_id,created_at,updated_at) VALUES(?,?,?,?,?,?)`, serverID, name, serverNameKey(name), ownerID, now, now); isUnique(err) {
		return model.Server{}, ErrServerNameTaken
	} else if err != nil {
		return model.Server{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO server_members(server_id,user_id,joined_at) VALUES(?,?,?)`, serverID, ownerID, now); err != nil {
		return model.Server{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO conversations(id,kind,created_at) VALUES(?,'channel',?)`, conversationID, now); err != nil {
		return model.Server{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO server_channels(id,server_id,conversation_id,name,position,creator_id,created_at,updated_at) VALUES(?,?,?,'general',0,?,?,?)`, channelID, serverID, conversationID, ownerID, now, now); err != nil {
		return model.Server{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Server{}, err
	}
	return m.Server(ctx, serverID, ownerID)
}

func (m *Module) Servers(ctx context.Context, userID string) ([]model.Server, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT s.id,s.name,CASE WHEN s.owner_user_id=? THEN 'owner' ELSE 'member' END,s.created_at,s.updated_at,sc.id,sc.server_id,sc.conversation_id,sc.name,sc.position,sc.created_at,sc.updated_at FROM servers s JOIN server_members sm ON sm.server_id=s.id AND sm.user_id=? JOIN server_channels sc ON sc.server_id=s.id AND sc.position=0 ORDER BY lower(s.name),s.id`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Server{}
	for rows.Next() {
		var s model.Server
		var c model.ServerChannel
		var created, updated, cc, cu string
		if err := rows.Scan(&s.ID, &s.Name, &s.Role, &created, &updated, &c.ID, &c.ServerID, &c.ConversationID, &c.Name, &c.Position, &cc, &cu); err != nil {
			return nil, err
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		s.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, cc)
		c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, cu)
		s.DefaultChannelID = c.ID
		s.Channels = []model.ServerChannel{c}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Module) Server(ctx context.Context, serverID, userID string) (model.Server, error) {
	items, err := m.Servers(ctx, userID)
	if err != nil {
		return model.Server{}, err
	}
	for _, s := range items {
		if s.ID == serverID {
			return s, nil
		}
	}
	return model.Server{}, ErrServerNotFound
}

func (m *Module) ServerChannels(ctx context.Context, serverID, userID string) ([]model.ServerChannel, error) {
	var member int
	if err := m.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_members WHERE server_id=? AND user_id=?)`, serverID, userID).Scan(&member); err != nil {
		return nil, err
	}
	if member != 1 {
		return nil, ErrServerNotFound
	}
	rows, err := m.db.QueryContext(ctx, `SELECT id,server_id,conversation_id,name,position,created_at,updated_at FROM server_channels WHERE server_id=? ORDER BY position,id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ServerChannel{}
	for rows.Next() {
		var c model.ServerChannel
		var a, b string
		if err := rows.Scan(&c.ID, &c.ServerID, &c.ConversationID, &c.Name, &c.Position, &a, &b); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, a)
		c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, b)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Module) ServerMembers(ctx context.Context, serverID, userID string) ([]model.ServerMember, error) {
	var member int
	if err := m.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_members WHERE server_id=? AND user_id=?)`, serverID, userID).Scan(&member); err != nil {
		return nil, err
	}
	if member != 1 {
		return nil, ErrServerNotFound
	}
	rows, err := m.db.QueryContext(ctx, `SELECT u.auth_user_id,u.username,u.display_name,CASE WHEN s.owner_user_id=u.auth_user_id THEN 'owner' ELSE 'member' END,sm.joined_at FROM server_members sm JOIN servers s ON s.id=sm.server_id JOIN relay_users u ON u.auth_user_id=sm.user_id WHERE sm.server_id=? ORDER BY CASE WHEN s.owner_user_id=u.auth_user_id THEN 0 ELSE 1 END,lower(u.username)`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ServerMember{}
	for rows.Next() {
		var v model.ServerMember
		var raw string
		if err := rows.Scan(&v.User.ID, &v.User.Username, &v.User.DisplayName, &v.Role, &raw); err != nil {
			return nil, err
		}
		v.JoinedAt, _ = time.Parse(time.RFC3339Nano, raw)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (m *Module) RenameServer(ctx context.Context, serverID, ownerID, name string) (model.Server, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Server{}, err
	}
	defer tx.Rollback()
	var found int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM servers WHERE id=? AND owner_user_id=?)`, serverID, ownerID).Scan(&found); err != nil {
		return model.Server{}, err
	}
	if found != 1 {
		return model.Server{}, ErrServerNotFound
	}
	if err = ensureServerNameAvailable(ctx, tx, ownerID, name, serverID); err != nil {
		return model.Server{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE servers SET name=?,name_key=?,updated_at=? WHERE id=?`, name, serverNameKey(name), nowText(), serverID); isUnique(err) {
		return model.Server{}, ErrServerNameTaken
	} else if err != nil {
		return model.Server{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Server{}, err
	}
	return m.Server(ctx, serverID, ownerID)
}

func serverNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func ensureServerNameAvailable(ctx context.Context, tx *sql.Tx, ownerID, name, excludeID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,name FROM servers WHERE owner_user_id=?`, ownerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, existing string
		if err = rows.Scan(&id, &existing); err != nil {
			return err
		}
		if id != excludeID && strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(name)) {
			return ErrServerNameTaken
		}
	}
	return rows.Err()
}

func (m *Module) CreateServerInvite(ctx context.Context, id, serverID, inviterID, inviteeID string, maxPerHour int) (model.ServerInvite, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ServerInvite{}, err
	}
	defer tx.Rollback()
	var member, target, friends, recent int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_members WHERE server_id=? AND user_id=?)`, serverID, inviterID).Scan(&member); err != nil {
		return model.ServerInvite{}, err
	}
	if member != 1 {
		return model.ServerInvite{}, ErrServerNotFound
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_members WHERE server_id=? AND user_id=?)`, serverID, inviteeID).Scan(&target); err != nil {
		return model.ServerInvite{}, err
	}
	if target == 1 {
		return model.ServerInvite{}, ErrAlreadyMember
	}
	low, high := canonicalPair(inviterID, inviteeID)
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM friendships WHERE user_low=? AND user_high=?)`, low, high).Scan(&friends); err != nil {
		return model.ServerInvite{}, err
	}
	if friends != 1 {
		return model.ServerInvite{}, ErrFriendshipRequired
	}
	since := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_invite_log WHERE inviter_id=? AND created_at>=?`, inviterID, since).Scan(&recent); err != nil {
		return model.ServerInvite{}, err
	}
	if recent >= maxPerHour {
		return model.ServerInvite{}, ErrInviteRateLimit
	}
	createdAt := nowText()
	if _, err = tx.ExecContext(ctx, `INSERT INTO server_invites(id,server_id,inviter_id,invitee_id,created_at) VALUES(?,?,?,?,?)`, id, serverID, inviterID, inviteeID, createdAt); isUnique(err) {
		return model.ServerInvite{}, ErrInviteExists
	} else if err != nil {
		return model.ServerInvite{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO server_invite_log(inviter_id,created_at) VALUES(?,?)`, inviterID, createdAt); err != nil {
		return model.ServerInvite{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.ServerInvite{}, err
	}
	return m.ServerInvite(ctx, id, inviteeID)
}

func (m *Module) ServerInvite(ctx context.Context, id, inviteeID string) (model.ServerInvite, error) {
	items, err := m.ServerInvites(ctx, inviteeID)
	if err != nil {
		return model.ServerInvite{}, err
	}
	for _, v := range items {
		if v.ID == id {
			return v, nil
		}
	}
	return model.ServerInvite{}, ErrInviteNotFound
}
func (m *Module) ServerInvites(ctx context.Context, userID string) ([]model.ServerInvite, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT i.id,i.created_at,s.id,s.name,s.created_at,s.updated_at,sc.id,u.auth_user_id,u.username,u.display_name FROM server_invites i JOIN servers s ON s.id=i.server_id JOIN server_channels sc ON sc.server_id=s.id AND sc.position=0 JOIN relay_users u ON u.auth_user_id=i.inviter_id WHERE i.invitee_id=? ORDER BY i.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ServerInvite{}
	for rows.Next() {
		var v model.ServerInvite
		var a, sc, su string
		if err := rows.Scan(&v.ID, &a, &v.Server.ID, &v.Server.Name, &sc, &su, &v.Server.DefaultChannelID, &v.Inviter.ID, &v.Inviter.Username, &v.Inviter.DisplayName); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, a)
		v.Server.CreatedAt, _ = time.Parse(time.RFC3339Nano, sc)
		v.Server.UpdatedAt, _ = time.Parse(time.RFC3339Nano, su)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (m *Module) AcceptServerInvite(ctx context.Context, id, userID string, maxMemberships int) (model.Server, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Server{}, err
	}
	defer tx.Rollback()
	var serverID string
	if err = tx.QueryRowContext(ctx, `SELECT server_id FROM server_invites WHERE id=? AND invitee_id=?`, id, userID).Scan(&serverID); errors.Is(err, sql.ErrNoRows) {
		return model.Server{}, ErrInviteNotFound
	} else if err != nil {
		return model.Server{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_members WHERE user_id=?`, userID).Scan(&count); err != nil {
		return model.Server{}, err
	}
	if count >= maxMemberships {
		return model.Server{}, ErrMembershipLimit
	}
	now := nowText()
	if _, err = tx.ExecContext(ctx, `INSERT INTO server_members(server_id,user_id,joined_at) VALUES(?,?,?)`, serverID, userID, now); isUnique(err) {
		return model.Server{}, ErrAlreadyMember
	} else if err != nil {
		return model.Server{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM server_invites WHERE id=?`, id); err != nil {
		return model.Server{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Server{}, err
	}
	return m.Server(ctx, serverID, userID)
}
func (m *Module) DeclineServerInvite(ctx context.Context, id, userID string) error {
	res, err := m.db.ExecContext(ctx, `DELETE FROM server_invites WHERE id=? AND invitee_id=?`, id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrInviteNotFound
	}
	return nil
}
func (m *Module) CancelServerInvite(ctx context.Context, id, userID string) (string, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var invitee string
	if err = tx.QueryRowContext(ctx, `SELECT invitee_id FROM server_invites WHERE id=? AND (inviter_id=? OR EXISTS(SELECT 1 FROM servers s WHERE s.id=server_invites.server_id AND s.owner_user_id=?))`, id, userID, userID).Scan(&invitee); errors.Is(err, sql.ErrNoRows) {
		return "", ErrInviteNotFound
	} else if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM server_invites WHERE id=?`, id); err != nil {
		return "", err
	}
	return invitee, tx.Commit()
}

func (m *Module) ServerInvitees(ctx context.Context, serverID, inviterID string) ([]string, error) {
	query := `SELECT DISTINCT invitee_id FROM server_invites WHERE server_id=?`
	args := []any{serverID}
	if inviterID != "" {
		query += ` AND inviter_id=?`
		args = append(args, inviterID)
	}
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (m *Module) RemoveServerMember(ctx context.Context, serverID, ownerID, targetID string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT owner_user_id FROM servers WHERE id=?`, serverID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return ErrServerNotFound
	} else if err != nil {
		return err
	}
	if owner != ownerID {
		return ErrOwnerRequired
	}
	if targetID == owner {
		return ErrOwnerCannotLeave
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM server_members WHERE server_id=? AND user_id=?`, serverID, targetID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrServerNotFound
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM server_invites WHERE server_id=? AND inviter_id=?`, serverID, targetID); err != nil {
		return err
	}
	return tx.Commit()
}
func (m *Module) LeaveServer(ctx context.Context, serverID, userID string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT owner_user_id FROM servers WHERE id=?`, serverID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return ErrServerNotFound
	} else if err != nil {
		return err
	}
	if owner == userID {
		return ErrOwnerCannotLeave
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM server_members WHERE server_id=? AND user_id=?`, serverID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrServerNotFound
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM server_invites WHERE server_id=? AND inviter_id=?`, serverID, userID); err != nil {
		return err
	}
	return tx.Commit()
}
func (m *Module) TransferServer(ctx context.Context, serverID, ownerID, targetID string) error {
	res, err := m.db.ExecContext(ctx, `UPDATE servers SET owner_user_id=?,updated_at=? WHERE id=? AND owner_user_id=? AND EXISTS(SELECT 1 FROM server_members WHERE server_id=? AND user_id=?)`, targetID, nowText(), serverID, ownerID, serverID, targetID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrServerNotFound
	}
	return nil
}

func (m *Module) DeleteServer(ctx context.Context, serverID, ownerID, confirmation string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name string
	if err = tx.QueryRowContext(ctx, `SELECT name FROM servers WHERE id=? AND owner_user_id=?`, serverID, ownerID).Scan(&name); errors.Is(err, sql.ErrNoRows) {
		return ErrServerNotFound
	} else if err != nil {
		return err
	}
	if confirmation != name {
		return ErrConfirmation
	}
	rows, err := tx.QueryContext(ctx, `SELECT conversation_id FROM server_channels WHERE server_id=?`, serverID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE conversation_id=?`, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM servers WHERE id=?`, serverID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `DELETE FROM conversations WHERE id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *Module) ChannelAccess(ctx context.Context, conversationID, userID string) (model.ChannelAccess, error) {
	var access model.ChannelAccess
	err := m.db.QueryRowContext(ctx, `SELECT sc.conversation_id,sc.server_id FROM server_channels sc JOIN server_members sm ON sm.server_id=sc.server_id AND sm.user_id=? WHERE sc.conversation_id=?`, userID, conversationID).Scan(&access.ConversationID, &access.ServerID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ChannelAccess{}, ErrConversationNotFound
	} else if err != nil {
		return model.ChannelAccess{}, err
	}
	rows, err := m.db.QueryContext(ctx, `SELECT user_id FROM server_members WHERE server_id=?`, access.ServerID)
	if err != nil {
		return model.ChannelAccess{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return model.ChannelAccess{}, err
		}
		access.MemberIDs = append(access.MemberIDs, id)
	}
	return access, rows.Err()
}
