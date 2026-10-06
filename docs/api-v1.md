# Relay client API v1

Relay's browser client and future clients share JSON resource contracts under `/api/v1`. Browser authentication is transported in HttpOnly cookies. Domain modules receive a validated principal and do not inspect cookies.

All private responses use `Cache-Control: no-store`. Mutation bodies require JSON, are limited to 16 KiB, and require a same-origin browser request. Errors are `{"error":{"code":"...","message":"...","field":"..."}}`.

Authentication can be restored from the HttpOnly refresh cookie when the access cookie is absent or near expiry. Relay rotates both cookies through Supabase and coalesces simultaneous refreshes from multiple tabs. Invalid or revoked sessions return `401 session_expired`; temporary provider/network/rate-limit/5xx failures return `503 auth_unavailable`.

## Foundation resources

`GET /api/v1/bootstrap` returns the current account/profile, capability flags, permanent global-lobby identity, and aggregate pending/unread counts.

`GET /api/v1/me`, `PATCH /api/v1/me/display-name`, `POST /api/v1/me/username`, and `POST /api/v1/me/password` are versioned aliases for account settings.

`GET /api/v1/conversations/{conversationId}/messages` accepts `limit` (1–100, default 50) and an optional exclusive `before` integer message-ID cursor.

`POST /api/v1/conversations/{conversationId}/messages` accepts only `{"text":"..."}`. Sender identity always comes from the authenticated principal. Extra identity fields are rejected.

## Realtime

Connect to `/api/v1/realtime` after a successful `/api/v1/me` or bootstrap request. The socket is receive-only; create resources through HTTP.

```json
{
  "version": 1,
  "eventId": "019...",
  "type": "message.created",
  "occurredAt": "2026-10-05T12:00:00Z",
  "data": {"message": {}}
}
```

Events never contain email, tokens, Supabase metadata, DOM identifiers, CSS selectors, or SQLite row representations. A user is online from their first active socket until their last socket disconnects. Slow clients and sessions rejected by periodic Supabase validation are disconnected.

## Friends

Friend lookup is exact and normalized; Relay provides no public directory or partial search. `GET /api/v1/users/lookup?username=...` returns public identity plus the caller's relationship state. Friends and pending requests are listed separately through `/api/v1/friends` and `/api/v1/friend-requests`.

Create with `POST /api/v1/friend-requests` and `{"username":"exact_name"}`. Accept and decline are recipient-only, cancel is requester-only, and either friend can remove a friendship. Sending the same request is idempotent; crossed requests atomically create a friendship. Realtime types are `friend.request.created`, `friend.request.removed`, `friendship.created`, and `friendship.removed`.

## Direct messages

`GET /api/v1/direct-conversations` lists every canonical one-to-one conversation belonging to the caller, newest activity first. Each item includes the peer's public Relay identity, the newest message, unread count, send permission, and friendship-scoped presence when still friends. Email and Supabase metadata are never returned.

`POST /api/v1/direct-conversations` accepts `{"userId":"..."}`. The users must be accepted friends. It returns `201` when the canonical conversation is first created and `200` when the existing conversation is reused.

DM history and sending use the common conversation routes. Either participant can always read retained history. Sending requires an active friendship; former friends receive `409 friendship_required`, while non-participants receive `404 conversation_not_found`.

`PUT /api/v1/conversations/{conversationId}/read` accepts `{"messageId":123}`. The cursor advances monotonically only after Relay verifies that the message belongs to that conversation. Unread totals count only messages authored by the other participant after the cursor.

DM events are `conversation.created`, `message.created`, and `conversation.unread_updated`. They are routed only to the affected users' sockets. HTTP send responses and realtime events share a message ID so clients can deduplicate them.

## Servers

`GET /api/v1/servers` lists only servers in which the caller is currently a member. `POST /api/v1/servers` accepts `{"name":"..."}` and atomically creates the server, owner membership, channel conversation, and default `#general` channel. Server names are case-insensitively unique among servers owned by the same user; a duplicate returns `409 server_name_taken`. Different owners may use the same name.

`GET`, `PATCH`, and `DELETE /api/v1/servers/{serverId}` read, rename, or permanently delete a server. Deletion is owner-only and requires `{"name":"exact current name"}`. `GET /api/v1/servers/{serverId}/members` returns public Relay identities and effective owner/member roles. Owners remove a member with `DELETE /api/v1/servers/{serverId}/members/{userId}` and transfer ownership with `POST /api/v1/servers/{serverId}/ownership` plus `{"userId":"..."}`. Members leave through `POST /api/v1/servers/{serverId}/leave`; owners must transfer or delete first.

`GET /api/v1/servers/{serverId}/channels` lists current channels. Version 0.7 creates only `#general`; channel administration is deferred. Channel history and sending use the common conversation routes and require current membership. Private-resource failures return non-enumerating `404` responses where appropriate.

Members invite a current Relay friend through `POST /api/v1/servers/{serverId}/invites` with `{"userId":"..."}`. `GET /api/v1/server-invites` lists the caller's incoming invitations. Accept and decline use `POST /api/v1/server-invites/{inviteId}/accept` and `/decline`; the inviter or server owner cancels with `DELETE /api/v1/server-invites/{inviteId}`. Pending invitations remain valid if the friendship later ends.

Server realtime events are `server.created`, `server.updated`, `server.deleted`, `server.invite.created`, `server.invite.removed`, `server.membership.changed`, and `channel.created`. State is sent only to current members or the specifically affected invitee. Channel `message.created` events are targeted to current members and include `serverId` for routing.
