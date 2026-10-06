package persistence

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"relay/internal/model"
)

type ServerSearchParams struct {
	Text, FromUserID, ChannelID, MentionsUserID string
	Before                                      int64
	Limit                                       int
}

func (m *Module) SearchServerMessages(ctx context.Context, serverID, userID string, p ServerSearchParams) ([]model.ServerSearchResult, error) {
	if p.Limit <= 0 {
		p.Limit = 25
	}
	query := `SELECT m.id,m.conversation_id,m.kind,COALESCE(m.user_id,''),m.username,COALESCE(u.display_name,m.username),m.text,m.created_at,
		sc.id,sc.server_id,sc.conversation_id,sc.name,sc.position,sc.created_at,sc.updated_at
		FROM server_channels sc
		JOIN server_members sm ON sm.server_id=sc.server_id AND sm.user_id=?
		JOIN messages m ON m.conversation_id=sc.conversation_id
		LEFT JOIN relay_users u ON u.auth_user_id=m.user_id
		WHERE sc.server_id=?`
	args := []any{userID, serverID}
	if p.Before > 0 {
		query += ` AND m.id<?`
		args = append(args, p.Before)
	}
	if p.ChannelID != "" {
		query += ` AND sc.id=?`
		args = append(args, p.ChannelID)
	}
	if p.FromUserID != "" {
		query += ` AND m.user_id=? AND EXISTS(SELECT 1 FROM server_members author_member WHERE author_member.server_id=sc.server_id AND author_member.user_id=?)`
		args = append(args, p.FromUserID, p.FromUserID)
	}
	if strings.TrimSpace(p.Text) != "" {
		query += ` AND instr(lower(m.text),lower(?))>0`
		args = append(args, strings.TrimSpace(p.Text))
	}
	if p.MentionsUserID != "" {
		query += ` AND EXISTS(SELECT 1 FROM relay_users ru JOIN server_members mention_member ON mention_member.user_id=ru.auth_user_id AND mention_member.server_id=sc.server_id WHERE ru.auth_user_id=?) AND instr(lower(m.text),lower('@'||(SELECT username FROM relay_users WHERE auth_user_id=?)))>0`
		args = append(args, p.MentionsUserID, p.MentionsUserID)
	}
	query += ` ORDER BY m.id DESC LIMIT ?`
	args = append(args, p.Limit)
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.ServerSearchResult, 0, p.Limit)
	for rows.Next() {
		var v model.ServerSearchResult
		var messageTime, channelCreated, channelUpdated string
		if err := rows.Scan(&v.Message.ID, &v.Message.ConversationID, &v.Message.Kind, &v.Message.UserID, &v.Message.Username, &v.Message.DisplayName, &v.Message.Text, &messageTime,
			&v.Channel.ID, &v.Channel.ServerID, &v.Channel.ConversationID, &v.Channel.Name, &v.Channel.Position, &channelCreated, &channelUpdated); err != nil {
			return nil, err
		}
		v.Message.CreatedAt, _ = time.Parse(time.RFC3339Nano, messageTime)
		v.Channel.CreatedAt, _ = time.Parse(time.RFC3339Nano, channelCreated)
		v.Channel.UpdatedAt, _ = time.Parse(time.RFC3339Nano, channelUpdated)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		var member int
		if err := m.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_members WHERE server_id=? AND user_id=?)`, serverID, userID).Scan(&member); err != nil {
			return nil, err
		}
		if member != 1 {
			return nil, ErrServerNotFound
		}
	}
	return out, nil
}

func (m *Module) ChannelPins(ctx context.Context, channelID, userID string) ([]model.ChannelPin, error) {
	if _, _, err := m.channelPermission(ctx, channelID, userID); err != nil {
		return nil, err
	}
	rows, err := m.db.QueryContext(ctx, `SELECT p.channel_id,p.pinned_at,pb.auth_user_id,pb.username,pb.display_name,
		m.id,m.conversation_id,m.kind,COALESCE(m.user_id,''),m.username,COALESCE(author.display_name,m.username),m.text,m.created_at
		FROM channel_pins p JOIN messages m ON m.id=p.message_id JOIN relay_users pb ON pb.auth_user_id=p.pinned_by
		LEFT JOIN relay_users author ON author.auth_user_id=m.user_id WHERE p.channel_id=? ORDER BY p.pinned_at DESC,m.id DESC`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ChannelPin{}
	for rows.Next() {
		var pin model.ChannelPin
		var pinnedAt, createdAt string
		if err := rows.Scan(&pin.ChannelID, &pinnedAt, &pin.PinnedBy.ID, &pin.PinnedBy.Username, &pin.PinnedBy.DisplayName,
			&pin.Message.ID, &pin.Message.ConversationID, &pin.Message.Kind, &pin.Message.UserID, &pin.Message.Username, &pin.Message.DisplayName, &pin.Message.Text, &createdAt); err != nil {
			return nil, err
		}
		pin.PinnedAt, _ = time.Parse(time.RFC3339Nano, pinnedAt)
		pin.Message.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, pin)
	}
	return out, rows.Err()
}

func (m *Module) PinChannelMessage(ctx context.Context, channelID string, messageID int64, userID string) (model.ChannelPin, []string, error) {
	serverID, owner, err := m.channelPermission(ctx, channelID, userID)
	if err != nil || !owner {
		if err == nil {
			err = ErrOwnerRequired
		}
		return model.ChannelPin{}, nil, err
	}
	var valid int
	if err = m.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages m JOIN server_channels sc ON sc.conversation_id=m.conversation_id WHERE sc.id=? AND m.id=?)`, channelID, messageID).Scan(&valid); err != nil || valid != 1 {
		if err == nil {
			err = ErrConversationNotFound
		}
		return model.ChannelPin{}, nil, err
	}
	if _, err = m.db.ExecContext(ctx, `INSERT INTO channel_pins(channel_id,message_id,pinned_by,pinned_at) VALUES(?,?,?,?) ON CONFLICT(channel_id,message_id) DO NOTHING`, channelID, messageID, userID, nowText()); err != nil {
		return model.ChannelPin{}, nil, err
	}
	pins, err := m.ChannelPins(ctx, channelID, userID)
	if err != nil {
		return model.ChannelPin{}, nil, err
	}
	var pin model.ChannelPin
	for _, item := range pins {
		if item.Message.ID == messageID {
			pin = item
			break
		}
	}
	audience, err := m.serverMemberIDs(ctx, serverID)
	return pin, audience, err
}

func (m *Module) UnpinChannelMessage(ctx context.Context, channelID string, messageID int64, userID string) ([]string, error) {
	serverID, owner, err := m.channelPermission(ctx, channelID, userID)
	if err != nil || !owner {
		if err == nil {
			err = ErrOwnerRequired
		}
		return nil, err
	}
	if _, err = m.db.ExecContext(ctx, `DELETE FROM channel_pins WHERE channel_id=? AND message_id=?`, channelID, messageID); err != nil {
		return nil, err
	}
	return m.serverMemberIDs(ctx, serverID)
}

func (m *Module) ChannelNotificationPreference(ctx context.Context, channelID, userID string) (model.ChannelNotificationPreference, error) {
	if _, _, err := m.channelPermission(ctx, channelID, userID); err != nil {
		return model.ChannelNotificationPreference{}, err
	}
	var item model.ChannelNotificationPreference
	var updated string
	err := m.db.QueryRowContext(ctx, `SELECT channel_id,mode,updated_at FROM channel_notification_preferences WHERE channel_id=? AND user_id=?`, channelID, userID).Scan(&item.ChannelID, &item.Mode, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ChannelNotificationPreference{ChannelID: channelID, Mode: "all"}, nil
	}
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return item, err
}

func (m *Module) SetChannelNotificationPreference(ctx context.Context, channelID, userID, mode string) (model.ChannelNotificationPreference, error) {
	if _, _, err := m.channelPermission(ctx, channelID, userID); err != nil {
		return model.ChannelNotificationPreference{}, err
	}
	now := nowText()
	if _, err := m.db.ExecContext(ctx, `INSERT INTO channel_notification_preferences(channel_id,user_id,mode,updated_at) VALUES(?,?,?,?) ON CONFLICT(channel_id,user_id) DO UPDATE SET mode=excluded.mode,updated_at=excluded.updated_at`, channelID, userID, mode, now); err != nil {
		return model.ChannelNotificationPreference{}, err
	}
	parsed, _ := time.Parse(time.RFC3339Nano, now)
	return model.ChannelNotificationPreference{ChannelID: channelID, Mode: mode, UpdatedAt: parsed}, nil
}

func (m *Module) channelPermission(ctx context.Context, channelID, userID string) (string, bool, error) {
	var serverID, ownerID string
	err := m.db.QueryRowContext(ctx, `SELECT sc.server_id,s.owner_user_id FROM server_channels sc JOIN servers s ON s.id=sc.server_id JOIN server_members sm ON sm.server_id=sc.server_id AND sm.user_id=? WHERE sc.id=?`, userID, channelID).Scan(&serverID, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrServerNotFound
	}
	return serverID, ownerID == userID, err
}

func (m *Module) serverMemberIDs(ctx context.Context, serverID string) ([]string, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT user_id FROM server_members WHERE server_id=?`, serverID)
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
