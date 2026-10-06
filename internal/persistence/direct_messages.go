package persistence

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"relay/internal/model"
)

func (m *Module) IsFriends(ctx context.Context, userID, otherID string) (bool, error) {
	low, high := canonicalPair(userID, otherID)
	var exists int
	err := m.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM friendships WHERE user_low=? AND user_high=?)`, low, high).Scan(&exists)
	return exists == 1, err
}

func (m *Module) CreateDirectConversation(ctx context.Context, id, userID, otherID string) (model.DirectConversation, bool, error) {
	if userID == otherID {
		return model.DirectConversation{}, false, ErrFriendshipRequired
	}
	low, high := canonicalPair(userID, otherID)
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return model.DirectConversation{}, false, err
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT conversation_id FROM direct_conversations WHERE user_low=? AND user_high=?`, low, high).Scan(&existingID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return model.DirectConversation{}, false, err
		}
		conversation, loadErr := m.DirectConversation(ctx, existingID, userID)
		return conversation, false, loadErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.DirectConversation{}, false, err
	}

	var friends int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM friendships WHERE user_low=? AND user_high=?)`, low, high).Scan(&friends); err != nil {
		return model.DirectConversation{}, false, err
	}
	if friends != 1 {
		return model.DirectConversation{}, false, ErrFriendshipRequired
	}
	now := nowText()
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversations(id,kind,created_at) VALUES(?,'direct',?)`, id, now); err != nil {
		return model.DirectConversation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO direct_conversations(conversation_id,user_low,user_high) VALUES(?,?,?)`, id, low, high); err != nil {
		return model.DirectConversation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_reads(conversation_id,user_id,last_read_message_id,updated_at) VALUES(?,?,0,?),(?,?,0,?)`, id, low, now, id, high, now); err != nil {
		return model.DirectConversation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.DirectConversation{}, false, err
	}
	conversation, err := m.DirectConversation(ctx, id, userID)
	return conversation, true, err
}

func (m *Module) DirectConversation(ctx context.Context, conversationID, userID string) (model.DirectConversation, error) {
	items, err := m.directConversations(ctx, userID, conversationID)
	if err != nil {
		return model.DirectConversation{}, err
	}
	if len(items) != 1 {
		return model.DirectConversation{}, ErrConversationNotFound
	}
	return items[0], nil
}

func (m *Module) DirectConversations(ctx context.Context, userID string) ([]model.DirectConversation, error) {
	return m.directConversations(ctx, userID, "")
}

func (m *Module) directConversations(ctx context.Context, userID, onlyID string) ([]model.DirectConversation, error) {
	query := `SELECT dc.conversation_id,c.created_at,
		u.auth_user_id,u.username,u.display_name,
		EXISTS(SELECT 1 FROM friendships f WHERE f.user_low=dc.user_low AND f.user_high=dc.user_high),
		COALESCE(cr.last_read_message_id,0),
		lm.id,COALESCE(lm.conversation_id,''),lm.kind,COALESCE(lm.user_id,''),lm.username,COALESCE(lu.display_name,lm.username),lm.text,lm.created_at,
		(SELECT COUNT(*) FROM messages um WHERE um.conversation_id=dc.conversation_id AND um.id>COALESCE(cr.last_read_message_id,0) AND COALESCE(um.user_id,'')<>?)
	FROM direct_conversations dc
	JOIN conversations c ON c.id=dc.conversation_id AND c.kind='direct'
	JOIN relay_users u ON u.auth_user_id=CASE WHEN dc.user_low=? THEN dc.user_high ELSE dc.user_low END
	LEFT JOIN conversation_reads cr ON cr.conversation_id=dc.conversation_id AND cr.user_id=?
	LEFT JOIN messages lm ON lm.id=(SELECT id FROM messages WHERE conversation_id=dc.conversation_id ORDER BY id DESC LIMIT 1)
	LEFT JOIN relay_users lu ON lu.auth_user_id=lm.user_id
	WHERE (dc.user_low=? OR dc.user_high=?)`
	args := []any{userID, userID, userID, userID, userID}
	if onlyID != "" {
		query += ` AND dc.conversation_id=?`
		args = append(args, onlyID)
	}
	query += ` ORDER BY COALESCE(lm.id,0) DESC,c.created_at DESC`
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DirectConversation{}
	for rows.Next() {
		var item model.DirectConversation
		var created string
		var friends int
		var lastRead int64
		var msgID sql.NullInt64
		var msgConversation, msgKind, msgUserID, msgUsername, msgDisplayName, msgText, msgCreated sql.NullString
		if err := rows.Scan(&item.ID, &created, &item.Peer.ID, &item.Peer.Username, &item.Peer.DisplayName, &friends, &lastRead,
			&msgID, &msgConversation, &msgKind, &msgUserID, &msgUsername, &msgDisplayName, &msgText, &msgCreated, &item.UnreadCount); err != nil {
			return nil, err
		}
		item.Kind = "direct"
		item.CanSend = friends == 1
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if msgID.Valid {
			createdAt, parseErr := time.Parse(time.RFC3339Nano, msgCreated.String)
			if parseErr != nil {
				return nil, parseErr
			}
			item.LastMessage = &model.Message{ID: msgID.Int64, ConversationID: msgConversation.String, Kind: msgKind.String, UserID: msgUserID.String, Username: msgUsername.String, DisplayName: msgDisplayName.String, Text: msgText.String, CreatedAt: createdAt}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (m *Module) DirectAccess(ctx context.Context, conversationID, userID string) (model.DirectAccess, error) {
	var low, high string
	err := m.db.QueryRowContext(ctx, `SELECT user_low,user_high FROM direct_conversations WHERE conversation_id=? AND (user_low=? OR user_high=?)`, conversationID, userID, userID).Scan(&low, &high)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DirectAccess{}, ErrConversationNotFound
	}
	if err != nil {
		return model.DirectAccess{}, err
	}
	peer := low
	if peer == userID {
		peer = high
	}
	friends, err := m.IsFriends(ctx, userID, peer)
	if err != nil {
		return model.DirectAccess{}, err
	}
	return model.DirectAccess{ConversationID: conversationID, PeerID: peer, CanSend: friends}, nil
}

func (m *Module) AdvanceRead(ctx context.Context, conversationID, userID string, messageID int64) (model.ReadState, error) {
	if _, err := m.DirectAccess(ctx, conversationID, userID); err != nil {
		return model.ReadState{}, err
	}
	var exists int
	if err := m.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE id=? AND conversation_id=?)`, messageID, conversationID).Scan(&exists); err != nil {
		return model.ReadState{}, err
	}
	if exists != 1 {
		return model.ReadState{}, ErrInvalidReadCursor
	}
	now := nowText()
	if _, err := m.db.ExecContext(ctx, `UPDATE conversation_reads SET last_read_message_id=CASE WHEN last_read_message_id<? THEN ? ELSE last_read_message_id END,updated_at=CASE WHEN last_read_message_id<? THEN ? ELSE updated_at END WHERE conversation_id=? AND user_id=?`, messageID, messageID, messageID, now, conversationID, userID); err != nil {
		return model.ReadState{}, err
	}
	return m.ReadState(ctx, conversationID, userID)
}

func (m *Module) ReadState(ctx context.Context, conversationID, userID string) (model.ReadState, error) {
	var state model.ReadState
	var raw string
	err := m.db.QueryRowContext(ctx, `SELECT cr.conversation_id,cr.last_read_message_id,cr.updated_at,
		(SELECT COUNT(*) FROM messages m WHERE m.conversation_id=cr.conversation_id AND m.id>cr.last_read_message_id AND COALESCE(m.user_id,'')<>cr.user_id)
		FROM conversation_reads cr JOIN direct_conversations dc ON dc.conversation_id=cr.conversation_id
		WHERE cr.conversation_id=? AND cr.user_id=? AND (dc.user_low=? OR dc.user_high=?)`, conversationID, userID, userID, userID).Scan(&state.ConversationID, &state.LastReadMessageID, &raw, &state.UnreadCount)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ReadState{}, ErrConversationNotFound
	}
	if err != nil {
		return model.ReadState{}, err
	}
	state.UpdatedAt, err = time.Parse(time.RFC3339Nano, raw)
	return state, err
}

func (m *Module) DirectUnreadTotals(ctx context.Context, userID string) (messages, conversations int, err error) {
	err = m.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(unread),0),COALESCE(SUM(CASE WHEN unread>0 THEN 1 ELSE 0 END),0) FROM (
		SELECT (SELECT COUNT(*) FROM messages m WHERE m.conversation_id=dc.conversation_id AND m.id>COALESCE(cr.last_read_message_id,0) AND COALESCE(m.user_id,'')<>?) unread
		FROM direct_conversations dc LEFT JOIN conversation_reads cr ON cr.conversation_id=dc.conversation_id AND cr.user_id=?
		WHERE dc.user_low=? OR dc.user_high=?
	)`, userID, userID, userID, userID).Scan(&messages, &conversations)
	return
}
