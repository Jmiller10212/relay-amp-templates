package persistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestServerLifecycleAndChannelAuthorization(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := New(dir)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer store.Stop(ctx)
	for _, u := range []struct{ id, name string }{{"a", "alice"}, {"b", "bob"}, {"c", "carol"}} {
		if _, err := store.CreateProfile(ctx, u.id, u.name, u.name); err != nil {
			t.Fatal(err)
		}
	}
	request, _, err := store.CreateFriendRequest(ctx, "friend", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AcceptFriendRequest(ctx, request.ID, "b"); err != nil {
		t.Fatal(err)
	}
	server, err := store.CreateServer(ctx, "server-1", "conversation-1", "channel-1", "a", "Test Server", 20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if server.Role != "owner" || len(server.Channels) != 1 || server.Channels[0].Name != "general" {
		t.Fatalf("server=%+v", server)
	}
	if _, err = store.ChannelAccess(ctx, "conversation-1", "c"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("outsider access=%v", err)
	}
	invite, err := store.CreateServerInvite(ctx, "invite-1", server.ID, "a", "b", 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateServerInvite(ctx, "invite-2", server.ID, "a", "b", 30); !errors.Is(err, ErrInviteExists) {
		t.Fatalf("duplicate invite=%v", err)
	}
	joined, err := store.AcceptServerInvite(ctx, invite.ID, "b", 100)
	if err != nil || joined.Role != "member" {
		t.Fatalf("joined=%+v err=%v", joined, err)
	}
	access, err := store.ChannelAccess(ctx, "conversation-1", "b")
	if err != nil || len(access.MemberIDs) != 2 {
		t.Fatalf("access=%+v err=%v", access, err)
	}
	if _, err = store.InsertConversation(ctx, "conversation-1", "user", "a", "alice", "Alice", "hello"); err != nil {
		t.Fatal(err)
	}
	if err = store.TransferServer(ctx, server.ID, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err = store.LeaveServer(ctx, server.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ChannelAccess(ctx, "conversation-1", "a"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("former member access=%v", err)
	}
	if err = store.DeleteServer(ctx, server.ID, "b", "wrong"); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("confirmation=%v", err)
	}
	if err = store.DeleteServer(ctx, server.ID, "b", "Test Server"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Conversation(ctx, "conversation-1"); err == nil {
		t.Fatal("deleted channel conversation remains")
	}
	if _, err = store.Conversation(ctx, "00000000-0000-7000-8000-000000000001"); err != nil {
		t.Fatalf("global conversation damaged: %v", err)
	}
}

func TestMigrationSevenCreatesBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := New(dir)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP INDEX server_channels_server; DROP TABLE server_channels; DROP INDEX server_invite_log_inviter; DROP TABLE server_invite_log; DROP INDEX server_invites_inviter; DROP INDEX server_invites_invitee; DROP TABLE server_invites; DROP INDEX server_members_user; DROP TABLE server_members; DROP INDEX servers_owner; DROP TABLE servers; DELETE FROM schema_migrations WHERE version=7`); err != nil {
		t.Fatal(err)
	}
	if err := store.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	store = New(dir)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer store.Stop(ctx)
	backups, err := filepath.Glob(filepath.Join(dir, "relay.db.pre-v7-*.backup"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
}

func TestServerInviteRateLimitSurvivesDecline(t *testing.T) {
	ctx := context.Background()
	store := New(t.TempDir())
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer store.Stop(ctx)
	for _, user := range []struct{ id, username string }{{"owner", "owner"}, {"first", "first"}, {"second", "second"}} {
		if _, err := store.CreateProfile(ctx, user.id, user.username, user.username); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"first", "second"} {
		request, _, err := store.CreateFriendRequest(ctx, "friend-"+target, "owner", target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.AcceptFriendRequest(ctx, request.ID, target); err != nil {
			t.Fatal(err)
		}
	}
	server, err := store.CreateServer(ctx, "server-rate", "conversation-rate", "channel-rate", "owner", "Rate Test", 20, 100)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateServerInvite(ctx, "invite-first", server.ID, "owner", "first", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DeclineServerInvite(ctx, invite.ID, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateServerInvite(ctx, "invite-second", server.ID, "owner", "second", 1); !errors.Is(err, ErrInviteRateLimit) {
		t.Fatalf("rate limit after decline=%v", err)
	}
}
