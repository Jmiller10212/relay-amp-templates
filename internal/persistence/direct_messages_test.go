package persistence

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func makeFriends(t *testing.T, store *Module, ctx context.Context, a, b string) {
	t.Helper()
	request, _, err := store.CreateFriendRequest(ctx, "request-"+a+"-"+b, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptFriendRequest(ctx, request.ID, b); err != nil {
		t.Fatal(err)
	}
}

func TestDirectConversationCanonicalUnreadAndFriendshipLifecycle(t *testing.T) {
	store, ctx := friendStore(t)
	makeFriends(t, store, ctx, "a", "b")
	conversation, created, err := store.CreateDirectConversation(ctx, "dm-one", "a", "b")
	if err != nil || !created || conversation.Peer.ID != "b" || !conversation.CanSend {
		t.Fatalf("conversation=%+v created=%v err=%v", conversation, created, err)
	}
	reused, created, err := store.CreateDirectConversation(ctx, "dm-two", "b", "a")
	if err != nil || created || reused.ID != conversation.ID {
		t.Fatalf("reused=%+v created=%v err=%v", reused, created, err)
	}
	if _, err := store.DirectAccess(ctx, conversation.ID, "c"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("third-party access err=%v", err)
	}
	message, err := store.InsertConversation(ctx, conversation.ID, "user", "a", "alice", "Alice", "private hello")
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.ReadState(ctx, conversation.ID, "b")
	if err != nil || state.UnreadCount != 1 {
		t.Fatalf("unread=%+v err=%v", state, err)
	}
	if _, err := store.AdvanceRead(ctx, conversation.ID, "b", message.ID); err != nil {
		t.Fatal(err)
	}
	state, _ = store.ReadState(ctx, conversation.ID, "b")
	if state.UnreadCount != 0 || state.LastReadMessageID != message.ID {
		t.Fatalf("advanced=%+v", state)
	}
	if err := store.RemoveFriend(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	access, err := store.DirectAccess(ctx, conversation.ID, "a")
	if err != nil || access.CanSend {
		t.Fatalf("unfriended access=%+v err=%v", access, err)
	}
	makeFriends(t, store, ctx, "b", "a")
	resumed, created, err := store.CreateDirectConversation(ctx, "dm-three", "a", "b")
	if err != nil || created || resumed.ID != conversation.ID || !resumed.CanSend {
		t.Fatalf("resumed=%+v created=%v err=%v", resumed, created, err)
	}
}

func TestDirectConversationConcurrentCreationReturnsOneConversation(t *testing.T) {
	store, ctx := friendStore(t)
	makeFriends(t, store, ctx, "a", "b")
	const attempts = 12
	ids := make(chan string, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conversation, _, err := store.CreateDirectConversation(ctx, fmt.Sprintf("candidate-%d", i), "a", "b")
			if err != nil {
				errs <- err
				return
			}
			ids <- conversation.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatalf("conversation IDs=%v", unique)
	}
}

func TestMigrationSixCreatesBackupAndPersistsDirectMessages(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := New(dir)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct{ id, username string }{{"a", "alice"}, {"b", "bob"}} {
		if _, err := store.CreateProfile(ctx, user.id, user.username, user.username); err != nil {
			t.Fatal(err)
		}
	}
	makeFriends(t, store, ctx, "a", "b")
	if _, err := store.db.ExecContext(ctx, `DROP TABLE channel_notification_preferences; DROP TABLE channel_pins; DROP INDEX messages_conversation_user_id; DROP INDEX server_channels_server_conversation; DROP INDEX server_channels_server; DROP TABLE server_channels; DROP INDEX server_invite_log_inviter; DROP TABLE server_invite_log; DROP INDEX server_invites_inviter; DROP INDEX server_invites_invitee; DROP TABLE server_invites; DROP INDEX server_members_user; DROP TABLE server_members; DROP INDEX servers_owner; DROP TABLE servers; DROP INDEX conversation_reads_user; DROP TABLE conversation_reads; DROP INDEX direct_conversations_high; DROP INDEX direct_conversations_low; DROP TABLE direct_conversations; DELETE FROM schema_migrations WHERE version>=6`); err != nil {
		t.Fatal(err)
	}
	if err := store.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	store = New(dir)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	conversation, _, err := store.CreateDirectConversation(ctx, "persisted-dm", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertConversation(ctx, conversation.ID, "user", "a", "alice", "alice", "survives restart"); err != nil {
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
	history, err := store.RecentConversation(ctx, conversation.ID, 0, 10)
	if err != nil || len(history) != 1 || history[0].Text != "survives restart" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "relay.db.pre-v6-*.backup"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
}
