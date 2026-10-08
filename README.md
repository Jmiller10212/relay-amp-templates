# Relay 0.7.3

Relay is a private real-time communication server. Version 0.7.3 adds member mention autocomplete, quiet per-server unread indicators, and a bundled notification sound used only for direct messages, server invitations, and actual `@username` mentions. It retains the focused server header, realtime member panel, scoped search, pins, private servers, Friends, and direct messages from 0.7.2. Relay remains one self-contained Go executable with an embedded responsive browser client, SQLite application storage, Supabase Auth, WebSockets, health reporting, and an AMP-friendly console.

## What is included

- Email/password registration and login through Supabase Auth
- Mandatory verification flow and generic resend/forgot-password responses
- Secure recovery callback and a 15-minute, one-use local recovery grant
- HttpOnly access/refresh cookies; the refresh cookie survives browser and Relay restarts while Supabase remains the session authority, and browser JavaScript never receives a token
- Permanent unique usernames and separate freely changeable display names
- Password-confirmed username changes with a seven-day default cooldown
- Password change with other Supabase sessions revoked
- Authenticated real-time chat, persisted history, multi-tab-aware presence, timestamps, and system broadcasts
- Versioned `/api/v1` client resources and a user-scoped realtime event stream
- Conversation-based storage with all legacy message IDs and global history preserved
- Build-free ES modules for API, realtime, state, authentication, conversation rendering, and settings
- Exact-username friend lookup, incoming/outgoing requests, crossed-request auto-accept, removal, badges, and friend-only presence
- One-to-one direct messages with one canonical conversation per friend pair, persistent unread cursors, and history retained read-only after unfriending
- Private servers with owner/member permissions, friend-only invitations, ownership transfer, leave/removal, typed deletion confirmation, and one automatic `#general` channel
- Server tools with a responsive member panel, server-scoped message search, owner-managed pins, mention autocomplete, and `all`, `mentions`, or `nothing` unread preferences
- Server messages use the shared persistence-first conversation pipeline and are routed only to current members
- Discord-inspired Relay shell with a 72px destination rail, 240px context sidebar, dense Friends views, searchable authorized DMs, and responsive mobile navigation
- Embedded Login, Create Account, Check Email, Forgot Password, Reset Password, Finish Profile, Chat, and Account Settings views
- SQLite migrations and an automatic pre-auth database backup
- `/health`, stdin console commands, graceful SIGINT/SIGTERM shutdown, and private AMP templates

## Responsibility boundary

| Component | Owns |
|---|---|
| Supabase Auth | Credentials, hashing, immutable Auth UUID, verification, sessions, token rotation/revocation, recovery tokens, password updates, Auth rate limits, and Auth security notifications |
| SMTP provider configured in Supabase | Verification, recovery, password-change, and future security-email delivery. Resend is the recommended production provider. |
| Relay Go server | Supabase BFF, HttpOnly cookies, same-origin checks, authenticated HTTP/WebSocket boundary, username policy, profile orchestration, and safe client/API errors |
| Relay SQLite | Supabase UUID linkage, username, display name, cooldown, pending registrations, one-use recovery grants, messages, friendships, DMs, private servers, channels, memberships, and invitations |
| Embedded browser client | Account and chat UI. It sends same-origin requests and never reads or stores Supabase tokens. |

Supabase application tables are intentionally unused. Relay does not store plaintext email, passwords, password hashes, verification tokens, Supabase tokens, or a parallel local login session.

## Run locally

Requirements for building are Go 1.24 or newer. Runtime has no external library requirement.

1. Copy `relay.example.json` to `relay.json`.
2. Set the two runtime environment variables:

   - `RELAY_SUPABASE_URL=https://your-project-ref.supabase.co`
   - `RELAY_SUPABASE_PUBLISHABLE_KEY=your-publishable-key`

   Environment variables take precedence. For an existing legacy AMP instance whose cached template cannot expose new environment settings, the same browser-safe values may instead be placed under `authentication.supabase_url` and `authentication.supabase_publishable_key` in `relay.json`. Never put a Supabase secret/service-role key, SMTP password, Resend key, or user token there.

3. Set `authentication.public_base_url` in `relay.json` to the exact Relay origin, such as `http://127.0.0.1:8080` for local testing.
4. Run `relay-server --config relay.json`.
5. Open the printed address. A successful release build prints `RELAY READY address=... version=0.7.3`.

Useful flags override JSON: `--config`, `--data-dir`, `--listen`, `--port`, and `--version`. Precedence is command line, then JSON, then defaults.

The server accepts these console commands:

- `help`
- `status`
- `users`
- `events`
- `broadcast <message>`
- `stop`

`stop`, SIGINT, and SIGTERM all use the bounded graceful-shutdown path.

## Supabase project setup

Use a dedicated hosted Supabase project for Relay.

1. Enable email/password sign-up and require email confirmation.
2. Keep current Supabase password-security defaults and Auth rate limits enabled. New projects should use asymmetric JWT signing; Relay still validates sensitive requests through the Auth `/user` endpoint instead of trusting decoded claims.
3. Set the Supabase Site URL to Relay's exact `authentication.public_base_url`.
4. Add the exact callback URL to Auth's redirect allowlist:

   `https://relay.example/auth/callback`

   Add explicit development/private-AMP callback URLs separately. Do not use production wildcards.
5. Use server-readable email links. The confirmation template link should be:

   `{{ .RedirectTo }}?token_hash={{ .TokenHash }}&type=email`

   The recovery template link should be:

   `{{ .RedirectTo }}?token_hash={{ .TokenHash }}&type=recovery`

6. Enable confirmation, recovery, reauthentication, and password-changed/security notification templates as appropriate.
7. Disable click tracking for authentication messages so the provider does not rewrite secure links.

Relay accepts any successful 2xx response from relevant Auth endpoints. Its provider implementation is a narrow standard-library HTTP client rather than the pre-production community Go Auth client.

### Production email with Resend

Configure Resend as Supabase custom SMTP. Relay itself does not call Resend and does not need a Resend key or SMTP password. This makes a later email-provider change a Supabase configuration change rather than an application redesign.

In Resend, verify the sending domain and disable click tracking for the authentication stream. In Supabase Auth SMTP settings, enter the Resend SMTP host, port, username, password, and verified sender. These values remain in Supabase; never place them in `relay.json`, AMP Relay settings, packages, source control, or Relay logs.

The unresolved production inputs are the public Relay domain, verified sender domain/DNS records, sender address, and Resend credentials. Do not expose the server publicly until TLS, redirect allowlists, and custom SMTP are configured.

## Secrets and browser-safe values

| Value | Browser-safe? | Relay runtime? |
|---|---:|---:|
| Supabase project URL/reference | Yes | `RELAY_SUPABASE_URL` |
| Supabase publishable key | Yes | `RELAY_SUPABASE_PUBLISHABLE_KEY` (never emitted to the Relay client) |
| Redirect/Site URLs | Yes | `authentication.public_base_url` |
| Access/refresh tokens | No | HttpOnly cookies only |
| Supabase secret/service-role key | No | Not used by Relay |
| Management API token | No | Setup time only; not used by Relay |
| Resend API key / SMTP password | No | Stored only in Supabase SMTP configuration |

Relay intentionally has no setting for a service-role key, Management token, Resend key, or SMTP password.

## Configuration

The example file documents the complete shape. Important authentication fields are:

- `registration_enabled`: enables new account creation.
- `public_base_url`: exact origin used for email callbacks.
- `cookie_secure_mode`: `auto`, `always`, or `never`. Use `always` for production HTTPS; `never` is only for private HTTP testing.
- `username_min_runes` / `username_max_runes`: default 3 and 32. Usernames are lowercase ASCII `a-z`, `0-9`, and `_`.
- `display_name_min_runes` / `display_name_max_runes`: Unicode plain-text display-name limits.
- `username_cooldown_hours`: default 168 (seven days).
- `reservation_lifetime_hours`: default 24.
- `recovery_grant_minutes`: default 15.
- `rate_limit_attempts` and `rate_limit_window_seconds`: Relay's per-IP BFF limit; Supabase limits remain a second layer.
- `session_validation_seconds`: open-WebSocket Supabase revalidation interval.

Server limits are `server_name_max_runes` (100), `max_owned_servers` (20), `max_server_memberships` (100), and `server_invites_per_hour` (30). Server names are trimmed Unicode plain text without control characters. A user cannot own two servers with the same case-insensitive name, while different owners may use the same name. Existing duplicates from 0.7.0 are preserved but no new matching duplicate can be created. Invitations may only target an existing friend. The relationship may end afterward without invalidating an already-issued invitation.

The obsolete `allow_duplicate_names` and top-level `username_max_runes` keys remain accepted for upgrade compatibility. The account system's username/display-name settings are authoritative in 0.7.3.

Modules are compile-time internal modules. `accounts` requires `persistence`; `realtime` requires `accounts`; `friends` requires persistence, accounts, and realtime; `chat` requires accounts and persistence; `direct_messages` requires persistence, accounts, realtime, friends, and chat; `servers` requires persistence, accounts, realtime, friends, and chat; the versioned client API requires chat and realtime; `health` requires persistence. Invalid enabled combinations fail clearly during startup. Direct messages and servers are enabled by default for old configuration files that omit the newer switches.

## HTTP and WebSocket protocol

The embedded client uses these stable 0.4 routes:

- `GET /api/v1/bootstrap` — current profile, feature capabilities, lobby identity, and aggregate badge counts.
- `GET /api/v1/me` and the versioned display-name, username, and password mutations.
- `GET /api/v1/presence` — authenticated global-lobby roster.
- `GET /api/v1/conversations/{id}/messages?limit=50&before=<message-id>` — paginated history.
- `POST /api/v1/conversations/{id}/messages` with `{"text":"..."}` — persistence-first message creation.
- `GET /api/v1/realtime` — authenticated receive-only WebSocket event stream.

Realtime envelopes contain `version`, UUID `eventId`, `type`, UTC `occurredAt`, and resource-neutral `data`. Version 0.4 emits `realtime.ready`, `profile.updated`, `presence.changed`, `message.created`, and `error`. HTTP send responses and matching realtime events use the same message ID so clients can deduplicate them.

The existing `/api/auth/*`, `/api/account/*`, and `/ws` endpoints remain compatibility aliases through the 0.8 line.

Friends resources are `GET /api/v1/users/lookup?username=...`, `GET /api/v1/friends`, `GET/POST /api/v1/friend-requests`, request accept/decline/cancel routes, and `DELETE /api/v1/friends/{userId}`. Lookup is exact only and returns public Relay identity—never email or Supabase metadata. Crossed requests atomically create one friendship.

Direct-message resources are `GET/POST /api/v1/direct-conversations`, the common conversation history/send routes, and `PUT /api/v1/conversations/{id}/read`. Only friends may create or send to a DM. Either participant retains history after unfriending, but sending returns `friendship_required`; re-friending restores sending in the same conversation. Non-participants receive `conversation_not_found` and cannot infer that a conversation exists.

Server resources are `GET/POST /api/v1/servers`, server details/rename/delete, members, ownership transfer, leave/removal, channel listing, friend invitations, and incoming invitation actions. Creating a server atomically creates its owner membership, channel conversation, and default `#general`. Current membership is required for channel history and sends. Owners alone may rename, remove members, transfer ownership, or permanently delete a server. An owner must transfer ownership or delete the server before leaving.

Server search is `GET /api/v1/servers/{serverId}/search` and remains bounded to channels in a server where the caller is currently a member. The embedded client supports free text plus `from:username`, `in:channel`, and `mentions:username`; typing `@` in a composer opens an authorized member picker and inserts the selected username. Mention matching remains literal case-insensitive `@username` text because structured mentions are not implemented. Ordinary inactive-channel messages update an unread badge without interrupting the user. Direct messages, server invitations, and exact mentions additionally show a clickable notice and play the bundled sound. Message context, pins, and notification-preference resources are documented in [docs/api-v1.md](docs/api-v1.md).

Account endpoints are under `/api/auth/*` and `/api/account/*`. Errors use:

```json
{"error":{"code":"invalid_username","message":"...","field":"username"}}
```

Authentication bodies are JSON-only and limited to 16 KiB. Cookie-authenticated mutations require the same Origin. Auth responses use `Cache-Control: no-store`.

`GET /api/account/me` and `GET /api/v1/me` can restore a session from the persistent refresh cookie when the short-lived access cookie is absent or near expiry. Concurrent refreshes are coalesced before Supabase token rotation. A revoked session returns `401 session_expired`; temporary Auth/network/rate-limit/5xx failures return `503 auth_unavailable`, so the client keeps its authenticated shell visible and retries instead of logging out. The browser calls the versioned endpoint before WebSocket connection/reconnection. `/ws` rejects unauthenticated upgrades and immediately sends `welcome`; there is no client `join` message. Client messages are only:

```json
{"type":"message","text":"hello"}
```

Identity-shaped client fields are rejected. Server message identity comes from the validated Supabase UUID and SQLite profile. Public messages expose only `id`, `kind`, optional `userId`, `username`, `displayName`, `text`, and `createdAt`.

## Storage, migration, backup, and restore

Persistent state is under `data_dir`:

- `relay.db`: SQLite application database
- `relay.db-wal` and `relay.db-shm`: SQLite runtime files when present
- `relay.db.pre-vN-YYYYMMDDTHHMMSSZ.backup`: automatic checkpointed copy created before schema-changing migrations

Back up `relay.json` and the entire data directory while Relay is stopped. To restore, stop Relay, replace those files from a matched backup, then start Relay. Existing messages are retained during the 0.3.0 migration with a null user ID; Relay never guesses which new account authored a legacy message. Migration 7 creates a checkpointed `relay.db.pre-v7-<UTC>.backup` before adding servers. Migration 9 creates `relay.db.pre-v9-<UTC>.backup` before adding pins and notification preferences. Server creation and deletion are transactional; deleting a server permanently deletes only that server's channel conversations and messages.

## AMP private repository installation

The `amp/` directory contains `relay.kvp`, settings manifest, metaconfig, port definition, update stages, and private-repository manifest. This template is for a private repository only and must not be submitted as an AI-generated public AMPTemplates contribution.

1. Publish `relay-amp-linux-amd64.zip` as an asset on a GitHub release. The supplied template defaults to the stable URL `https://github.com/Jmiller10212/relay-amp-templates/releases/latest/download/relay-amp-linux-amd64.zip`.
2. Place the `amp/` files in your private AMP configuration repository.
3. In ADS, open **Configuration → Instance Deployment**, add `owner/repository:branch`, fetch it, and refresh AMP.
4. Create a new Relay instance and let AMP allocate a TCP port.
5. Set Public Relay URL, Supabase Project URL, and the Supabase publishable key. The package URL normally needs no change.
6. Update the instance, then start it. AMP detects `^RELAY READY(?: .*)?$`, provides readable/writable STDIO, and stops cleanly by sending `stop`.

### Updating Relay in AMP

Stop Relay, click **Update**, and start it again. The configured package URL follows the latest GitHub release automatically. The update archive contains only package-managed files and deliberately omits `relay.json` and `data/`, so application settings, account profiles, chat history, and the SQLite database remain in place. A versioned release asset can still be entered temporarily when a rollback is required.

Older AMP instances cache the template version they were created from. Relay 0.7.3 therefore accepts the Supabase project URL and publishable key from either AMP environment variables or the `authentication.supabase_url` and `authentication.supabase_publishable_key` compatibility fields in `relay.json`; environment variables win. New instances expose the full settings page directly, including Direct Messages, Servers, server limits, and invitation throttling.

Update archives deliberately omit `relay.json` and `data/`. AMP smart exclusion is disabled so package-managed files such as the executable update reliably; live configuration/database files are preserved because they are not in the archive.

## Architecture and adding a module

Every internal module implements name/dependencies, initialize/start/stop lifecycle hooks, and HTTP/WebSocket registration. The central registry validates enabled dependencies, starts in dependency order, and stops in reverse order.

To add a future feature module without changing unrelated core code:

1. Create `internal/<feature>` implementing the module interface.
2. Declare only its direct module dependencies.
3. Register its HTTP/WebSocket routes inside that package.
4. Add one constructor entry to `internal/modules/all/catalog.go`.
5. Add its enable switch to `config.Modules`, `relay.example.json`, and the AMP manifest.
6. Add package tests and registry dependency/lifecycle tests.

No HTTP server, persistence implementation, or chat code needs modification unless the new module explicitly integrates with it. Dynamic shared-library loading is intentionally deferred.

## Security limitations

Relay 0.7.3 includes real account authentication, persistent friends, one-to-one DMs, and private owner/member servers, but it is still an early private-network application. It has no MFA, social login, email-address changes, account deletion, avatars, group DMs, custom channels, granular roles/permissions, moderation UI, attachment scanning, end-to-end encryption, or multi-instance coordination. TLS termination is external to Relay. Server administrators can read SQLite message history. Keep it on a trusted private network until HTTPS, a production domain, redirect allowlists, custom SMTP, backups, monitoring, and operational access controls are in place.
