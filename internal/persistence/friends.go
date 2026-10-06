package persistence

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"relay/internal/model"
)

func canonicalPair(a, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

func (m *Module) UserByUsername(ctx context.Context, username string) (model.PublicUser, error) {
	var user model.PublicUser
	err := m.db.QueryRowContext(ctx, `SELECT auth_user_id,username,display_name FROM relay_users WHERE username=? COLLATE NOCASE`, username).Scan(&user.ID, &user.Username, &user.DisplayName)
	return user, err
}

func (m *Module) Relationship(ctx context.Context, userID, otherID string) (string, string, error) {
	low, high := canonicalPair(userID, otherID)
	var exists int
	if err := m.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM friendships WHERE user_low=? AND user_high=?)`, low, high).Scan(&exists); err != nil {
		return "", "", err
	}
	if exists == 1 {
		return "friends", "", nil
	}
	var id, requester string
	err := m.db.QueryRowContext(ctx, `SELECT id,requester_id FROM friend_requests WHERE user_low=? AND user_high=?`, low, high).Scan(&id, &requester)
	if errors.Is(err, sql.ErrNoRows) {
		return "none", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if requester == userID {
		return "outgoing", id, nil
	}
	return "incoming", id, nil
}

func (m *Module) CreateFriendRequest(ctx context.Context, id, requesterID, recipientID string) (model.FriendRequest, bool, error) {
	if requesterID == recipientID {
		return model.FriendRequest{}, false, errors.New("cannot friend yourself")
	}
	low, high := canonicalPair(requesterID, recipientID)
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return model.FriendRequest{}, false, err
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM friendships WHERE user_low=? AND user_high=?)`, low, high).Scan(&existing); err != nil {
		return model.FriendRequest{}, false, err
	}
	if existing == 1 {
		return model.FriendRequest{}, false, ErrAlreadyFriends
	}
	var oldID, oldRequester string
	err = tx.QueryRowContext(ctx, `SELECT id,requester_id FROM friend_requests WHERE user_low=? AND user_high=?`, low, high).Scan(&oldID, &oldRequester)
	if err == nil {
		if oldRequester == requesterID {
			request, loadErr := friendRequestTx(ctx, tx, oldID)
			if loadErr != nil {
				return model.FriendRequest{}, false, loadErr
			}
			return request, false, tx.Commit()
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM friend_requests WHERE id=?`, oldID); err != nil {
			return model.FriendRequest{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO friendships(user_low,user_high,created_at) VALUES(?,?,?)`, low, high, nowText()); err != nil {
			return model.FriendRequest{}, false, err
		}
		return model.FriendRequest{}, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.FriendRequest{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO friend_requests(id,requester_id,recipient_id,user_low,user_high,created_at) VALUES(?,?,?,?,?,?)`, id, requesterID, recipientID, low, high, nowText()); err != nil {
		return model.FriendRequest{}, false, err
	}
	request, err := friendRequestTx(ctx, tx, id)
	if err != nil {
		return model.FriendRequest{}, false, err
	}
	return request, false, tx.Commit()
}

func friendRequestTx(ctx context.Context, tx *sql.Tx, id string) (model.FriendRequest, error) {
	var request model.FriendRequest
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT fr.id,r.auth_user_id,r.username,r.display_name,p.auth_user_id,p.username,p.display_name,fr.created_at FROM friend_requests fr JOIN relay_users r ON r.auth_user_id=fr.requester_id JOIN relay_users p ON p.auth_user_id=fr.recipient_id WHERE fr.id=?`, id).Scan(&request.ID, &request.Requester.ID, &request.Requester.Username, &request.Requester.DisplayName, &request.Recipient.ID, &request.Recipient.Username, &request.Recipient.DisplayName, &raw)
	if err == nil {
		request.CreatedAt, err = time.Parse(time.RFC3339Nano, raw)
	}
	return request, err
}

func (m *Module) FriendRequests(ctx context.Context, userID string) ([]model.FriendRequest, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT fr.id,r.auth_user_id,r.username,r.display_name,p.auth_user_id,p.username,p.display_name,fr.created_at FROM friend_requests fr JOIN relay_users r ON r.auth_user_id=fr.requester_id JOIN relay_users p ON p.auth_user_id=fr.recipient_id WHERE fr.requester_id=? OR fr.recipient_id=? ORDER BY fr.created_at`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.FriendRequest{}
	for rows.Next() {
		var request model.FriendRequest
		var raw string
		if err := rows.Scan(&request.ID, &request.Requester.ID, &request.Requester.Username, &request.Requester.DisplayName, &request.Recipient.ID, &request.Recipient.Username, &request.Recipient.DisplayName, &raw); err != nil {
			return nil, err
		}
		request.CreatedAt, _ = time.Parse(time.RFC3339Nano, raw)
		out = append(out, request)
	}
	return out, rows.Err()
}

func (m *Module) Friends(ctx context.Context, userID string) ([]model.PublicUser, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT u.auth_user_id,u.username,u.display_name FROM friendships f JOIN relay_users u ON u.auth_user_id=CASE WHEN f.user_low=? THEN f.user_high ELSE f.user_low END WHERE f.user_low=? OR f.user_high=? ORDER BY u.username COLLATE NOCASE`, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PublicUser{}
	for rows.Next() {
		var user model.PublicUser
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, user)
	}
	return out, rows.Err()
}

func (m *Module) AcceptFriendRequest(ctx context.Context, requestID, recipientID string) (string, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var requester string
	err = tx.QueryRowContext(ctx, `SELECT requester_id FROM friend_requests WHERE id=? AND recipient_id=?`, requestID, recipientID).Scan(&requester)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrFriendRequestNotFound
	}
	if err != nil {
		return "", err
	}
	low, high := canonicalPair(requester, recipientID)
	if _, err = tx.ExecContext(ctx, `DELETE FROM friend_requests WHERE id=?`, requestID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO friendships(user_low,user_high,created_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, low, high, nowText()); err != nil {
		return "", err
	}
	return requester, tx.Commit()
}

func (m *Module) DeleteFriendRequest(ctx context.Context, requestID, userID, mode string) (string, error) {
	column := "requester_id"
	if mode == "decline" {
		column = "recipient_id"
	}
	otherExpr := "recipient_id"
	if mode == "decline" {
		otherExpr = "requester_id"
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	query := `SELECT ` + otherExpr + ` FROM friend_requests WHERE id=? AND ` + column + `=?`
	err = tx.QueryRowContext(ctx, query, requestID, userID).Scan(&otherExpr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrFriendRequestNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM friend_requests WHERE id=?`, requestID); err != nil {
		return "", err
	}
	return otherExpr, tx.Commit()
}

func (m *Module) RemoveFriend(ctx context.Context, userID, otherID string) error {
	low, high := canonicalPair(userID, otherID)
	res, err := m.db.ExecContext(ctx, `DELETE FROM friendships WHERE user_low=? AND user_high=?`, low, high)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAlreadyFriends
	}
	return nil
}
