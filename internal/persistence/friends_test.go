package persistence

import (
	"context"
	"errors"
	"testing"
)

func friendStore(t *testing.T) (*Module, context.Context) {
	t.Helper()
	ctx := context.Background()
	store := New(t.TempDir())
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct{ id, name string }{{"a", "alice"}, {"b", "bob"}, {"c", "carol"}} {
		if _, err := store.CreateProfile(ctx, user.id, user.name, user.name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = store.Stop(ctx) })
	return store, ctx
}

func TestFriendRequestPermissionsAndAcceptance(t *testing.T) {
	store, ctx := friendStore(t)
	request, crossed, err := store.CreateFriendRequest(ctx, "request-1", "a", "b")
	if err != nil || crossed {
		t.Fatalf("create=%+v crossed=%v err=%v", request, crossed, err)
	}
	duplicate, _, err := store.CreateFriendRequest(ctx, "request-2", "a", "b")
	if err != nil || duplicate.ID != "request-1" {
		t.Fatalf("idempotent=%+v err=%v", duplicate, err)
	}
	if _, err := store.AcceptFriendRequest(ctx, "request-1", "c"); !errors.Is(err, ErrFriendRequestNotFound) {
		t.Fatalf("third party accepted: %v", err)
	}
	other, err := store.AcceptFriendRequest(ctx, "request-1", "b")
	if err != nil || other != "a" {
		t.Fatalf("accept other=%s err=%v", other, err)
	}
	relation, _, err := store.Relationship(ctx, "a", "b")
	if err != nil || relation != "friends" {
		t.Fatalf("relationship=%s err=%v", relation, err)
	}
}

func TestCrossedRequestsAtomicallyBecomeFriendship(t *testing.T) {
	store, ctx := friendStore(t)
	if _, _, err := store.CreateFriendRequest(ctx, "one", "a", "b"); err != nil {
		t.Fatal(err)
	}
	_, crossed, err := store.CreateFriendRequest(ctx, "two", "b", "a")
	if err != nil || !crossed {
		t.Fatalf("crossed=%v err=%v", crossed, err)
	}
	requests, err := store.FriendRequests(ctx, "a")
	if err != nil || len(requests) != 0 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	friends, err := store.Friends(ctx, "a")
	if err != nil || len(friends) != 1 || friends[0].ID != "b" {
		t.Fatalf("friends=%+v err=%v", friends, err)
	}
}

func TestFriendshipSurvivesUsernameChange(t *testing.T) {
	store, ctx := friendStore(t)
	if _, _, err := store.CreateFriendRequest(ctx, "one", "a", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptFriendRequest(ctx, "one", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUsername(ctx, "b", "renamed", 0); err != nil {
		t.Fatal(err)
	}
	friends, err := store.Friends(ctx, "a")
	if err != nil || len(friends) != 1 || friends[0].Username != "renamed" {
		t.Fatalf("friends=%+v err=%v", friends, err)
	}
}
