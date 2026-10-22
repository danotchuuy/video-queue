# Video Queue: design

A private tool for moderators of one or more VTubers to track reaction content.

- **Servers**: each streamer's Discord server is registered in the app as a *server*
  (display name, e.g. the streamer's name). Everything below belongs to exactly one server,
  so one moderator can manage several streamers in one place via a server switcher.
- **Series**: anime abridged series (and other multi-episode shows) she reacts to.
- **Videos**: individual episodes/videos. Each has a **venue**: `watch_party`
  (Discord watch party) or `stream` (reacted to live on stream). Each moves through:

  ```
  queued -> reacted -> editing -> patreon -> youtube
                  \-> skipped   (recorded, but not getting edited/posted)
  ```

  Reactions are recorded (`vod_url`), sometimes edited, posted to Patreon first
  (`patreon_url`, `patreon_at`), then YouTube (`youtube_url`, `youtube_at`).
- **Suggestions**: viewers post suggestions in a per-server Discord channel. The bot pulls
  those messages in. Mods sort them (newest / oldest / most reactions) and **approve**
  (creates a queued video) or **discard** them.

## Servers & access

- One Discord application with one **bot** (`DISCORD_BOT_TOKEN`, required). The bot is invited to
  every registered server. It reads suggestion channels, lists roles/channels for configuration,
  and checks members' roles.
- **App admins** (`APP_ADMIN_USER_IDS`) register servers (from the guilds the bot is in), configure
  them (name, mod roles, allowed users, suggestions channel), delete them, and can access every server.
- **Anyone can sign in** with Discord (`identify` scope only). A non-admin user can access a
  server if their ID is in its `allowed_user_ids`, or they're a member of that guild with any role
  in its `mod_role_ids`.
- Role checks go through the bot (`GET /guilds/{guild}/members/{user}`) and are cached in
  `server_access` for `ACCESS_CACHE_TTL` (default 10m). Losing the role revokes access within
  that window. Changing a server's roles/allowed users clears its cache.
- Servers a user can't access look like they don't exist (404), never 403.

## Layout

```
cmd/server/          main: config, wiring, static SPA serving, legacy import, graceful shutdown
internal/config/     env/.env loading
internal/store/      SQLite (modernc.org/sqlite). Schema: migrations/*.sql. Types: models.go.
internal/auth/       Discord OAuth login, sessions, Require middleware
internal/access/     Checker: may user X access server Y (admin / allowlist / cached role check)
internal/discord/    Bot REST client (messages, guilds, roles, channels, members) + suggestions poller
internal/api/        JSON REST handlers under /api
web/                 Vite + React + TypeScript SPA
```

Packages that consume the store, bot or checker declare **their own small interface** of the
methods they need, so they can be tested with fakes.

## Conventions

- Go stdlib `net/http` routing (`mux.HandleFunc("GET /api/servers/{sid}/videos/{id}", ...)`), `log/slog`.
- JSON in/out. Errors: `{"error": "message"}`. 400 validation, 401 not signed in, 403 admin-only
  route for a non-admin, 404 not found or no access, 409 conflict, 500 otherwise (log the real
  error, don't leak it), 502 for Discord upstream failures.
- Timestamps: RFC 3339 strings in JSON. Stored as fixed-width RFC 3339 nanosecond UTC text so
  string order is time order.
- IDs: SQLite rows are int64; Discord IDs are strings.
- Tests: stdlib `testing` only.

## Store (`internal/store`)

Schema: `migrations/001_init.sql` + `002_servers.sql`. `server_id` on series/videos/suggestions is
nullable in SQL only so pre-existing rows survive. The app always sets it.

**Every read/update/delete of series, videos and suggestions is scoped by server**: the
method takes `serverID` and matches `WHERE id = ? AND server_id = ?`, returning `ErrNotFound`
for a row in another server. This is the tenant boundary. Method set:

```go
// servers
ListServers(ctx) ([]Server, error)                       // ordered by name (case-insensitive), id
GetServer(ctx, id int64) (Server, error)
GetServerByGuild(ctx, guildID string) (Server, error)
CreateServer(ctx, in Server) (Server, error)             // ErrConflict if guild_id already registered
UpdateServer(ctx, id int64, p ServerPatch) (Server, error) // changing the channel resets suggestions_cursor;
                                                        // changing roles/allowed users deletes server_access rows
DeleteServer(ctx, id int64) error                       // cascades to all its content
SetSuggestionsCursor(ctx, serverID int64, cursor string) error
// ClaimUnassigned: legacy single-server import. In one transaction: find the server with
// in.GuildID or create it from in; set server_id on every series/video/suggestion where it is
// NULL; if the server's cursor is empty, copy kv["suggestions_last_message_id"] into it and
// delete that kv key. Returns the server and the number of rows claimed.
ClaimUnassigned(ctx, in Server) (Server, int64, error)

// access cache
GetAccess(ctx, serverID int64, userID string) (allowed bool, checkedAt time.Time, found bool, err error)
SetAccess(ctx, serverID int64, userID string, allowed bool, checkedAt time.Time) error

// users & sessions: unchanged

// series
ListSeries(ctx, serverID int64) ([]Series, error)
GetSeries(ctx, serverID, id int64) (Series, error)
CreateSeries(ctx, in Series) (Series, error)               // uses in.ServerID
UpdateSeries(ctx, serverID, id int64, p SeriesPatch) (Series, error)
DeleteSeries(ctx, serverID, id int64) error

// videos
ListVideos(ctx, f VideoFilter) ([]Video, error)            // f.ServerID required
GetVideo(ctx, serverID, id int64) (Video, error)
CreateVideo(ctx, in Video) (Video, error)                  // position = max+1 within the server
UpdateVideo(ctx, serverID, id int64, p VideoPatch) (Video, error)
DeleteVideo(ctx, serverID, id int64) error
ReorderVideos(ctx, serverID int64, ids []int64) error      // ErrNotFound if any id isn't in the server

// suggestions
UpsertSuggestion(ctx, in Suggestion) error                 // uses in.ServerID on insert
ListSuggestions(ctx, f SuggestionFilter) ([]Suggestion, error) // f.ServerID required
GetSuggestion(ctx, serverID, id int64) (Suggestion, error)
SetSuggestionStatus(ctx, serverID, id int64, status SuggestionStatus, reviewerID string, now time.Time) (Suggestion, error)
ApproveSuggestion(ctx, serverID, id int64, v Video, reviewerID string, now time.Time) (Suggestion, Video, error)
```

`var ErrConflict = errors.New("store: conflict")` is added next to `ErrNotFound`.
`kv`/`GetKV`/`SetKV` stay for compatibility but the poller no longer uses them.

## Auth (`internal/auth`)

- `GET /api/auth/login`: random `state` in a short-lived HttpOnly cookie `vq_oauth_state`
  (10 min, SameSite=Lax), redirect to `https://discord.com/oauth2/authorize` with
  `response_type=code`, `client_id`, `scope=identify`, `state`, `redirect_uri`.
- `GET /api/auth/callback`: verify the state (constant-time) and clear it; `error` param redirects to
  `/login?error=cancelled`. Exchange the code, then `GET /users/@me`. Upstream failure redirects to
  `/login?error=discord`. There's **no guild/role check here**; access is per server (see
  `internal/access`). `UpsertUser`, create a session (32 random bytes, base64url in cookie
  `vq_session`, HttpOnly, SameSite=Lax, Path=/, Secure per config, Max-Age = TTL; store only
  hex(SHA-256(token))), redirect to `/`.
- `POST /api/auth/logout`: delete session, expire cookie, 204.
- `Require`: session lookup puts the user in the context, otherwise 401. Mutations with a body must be
  `application/json` (415).
- `auth.Config` is `{ClientID, ClientSecret, RedirectURL, CookieSecure, SessionTTL}`.

## Access (`internal/access`)

```go
type Store interface {
    GetAccess(ctx, serverID int64, userID string) (allowed bool, checkedAt time.Time, found bool, err error)
    SetAccess(ctx, serverID int64, userID string, allowed bool, checkedAt time.Time) error
}
type Members interface {
    GuildMember(ctx, guildID, userID string) (discord.Member, error) // discord.ErrNotFound if not a member
}
func New(st Store, m Members, adminIDs []string, ttl time.Duration, log *slog.Logger) *Checker
func (c *Checker) IsAdmin(userID string) bool
func (c *Checker) CanAccess(ctx, userID string, srv store.Server) (bool, error)
```

`CanAccess`, in order: admin is true; in `srv.AllowedUserIDs` is true; no `ModRoleIDs` is false;
a cached entry younger than ttl gives the cached value; otherwise fetch the member. Not a member is
false; else any role in `ModRoleIDs`. Cache the result with now. If the fetch fails and a cached entry
younger than 24h exists, log and return the cached value; otherwise return the error.
Concurrent checks for the same (server, user) should share one Discord request
(a small in-package singleflight via mutex + map is fine).

## Discord (`internal/discord`)

Bot client (`Authorization: Bot <token>`, base `https://discord.com/api/v10`, 429 retry once,
non-2xx produces an error with status and a truncated body). Existing: `ChannelMessages`. New (`guilds.go`):

- `BotGuilds`: `GET /users/@me/guilds?limit=200&after=..`, paging until fewer than 200.
- `Guild`: `GET /guilds/{id}`.
- `GuildRoles`: `GET /guilds/{id}/roles`, excluding `@everyone` (id == guild id) and `managed`, sorted
  by position descending.
- `GuildTextChannels`: `GET /guilds/{id}/channels`, types 0 and 5, sorted by position then id.
- `GuildMember`: `GET /guilds/{id}/members/{user}`.
- 404, or 403 with Discord JSON error code 50001 (Missing Access), gives `ErrNotFound`.

Poller (multi-server):

```go
type SuggestionStore interface {
    ListServers(ctx) ([]store.Server, error)
    GetServer(ctx, id int64) (store.Server, error)
    UpsertSuggestion(ctx, store.Suggestion) error
    SetSuggestionsCursor(ctx, serverID int64, cursor string) error
}
func NewPoller(c *Client, st SuggestionStore, interval time.Duration, log *slog.Logger) *Poller
func (p *Poller) Run(ctx)                                  // sync all servers now, then every interval
func (p *Poller) SyncServer(ctx, serverID int64) error     // one server; no-op if it has no channel
```

Run iterates `ListServers` and syncs each server that has a `SuggestionsChannelID`. One server's
error is logged and doesn't stop the others. Syncs are serialized per server. The per-server sync
algorithm is the existing one, but the cursor comes from `srv.SuggestionsCursor` (empty means `"0"`)
and is saved with `SetSuggestionsCursor`, and each suggestion gets `ServerID` and that channel's
`ChannelID`. Before saving a page, re-read the server and stop if its channel changed meanwhile.

## REST API (`internal/api`)

```go
type Syncer func(ctx context.Context, serverID int64) error
type Deps struct {
    Store   Store   // interface of the store methods used
    Access  Access  // IsAdmin(userID) bool; CanAccess(ctx, userID, store.Server) (bool, error)
    Bot     Bot     // BotGuilds, Guild, GuildRoles, GuildTextChannels
    Sync    Syncer  // nil means syncing is disabled
    ClientID string // Discord application ID, for the bot invite URL
    Log     *slog.Logger
}
func New(d Deps) *API
func (a *API) Register(mux *http.ServeMux, require func(http.Handler) http.Handler)
```

All routes require auth. `{sid}` routes first load the server (`GetServer`) and check `CanAccess`.
Missing or no access gives 404 `"server not found"`; a `CanAccess` error gives 502. Admin routes give 403
for non-admins.

| Method & path | Who | Body / query | Response |
|---|---|---|---|
| `GET /api/me` | any | | `{user: User, is_admin: bool}` |
| `GET /api/servers` | any | | `Server[]` the user can access (admins: all). Access errors for one server are logged and that server is skipped |
| `GET /api/admin/guilds` | admin | | `{invite_url, guilds: [{id, name, icon, server_id: int\|null}]}`: the bot's guilds, marking registered ones. `invite_url` = `https://discord.com/oauth2/authorize?client_id=..&scope=bot&permissions=66560` |
| `POST /api/servers` | admin | `{guild_id, name?}` | 201 `Server`. `Guild()` must succeed (bot present, else 400 "bot is not in that server"). `name` defaults to the guild name; `guild_name`/`icon` come from Discord. 409 if already registered |
| `GET /api/servers/{sid}` | access | | `Server` |
| `PATCH /api/servers/{sid}` | admin | `ServerPatch` | `Server`. Name trimmed, non-empty, max 100. IDs must be numeric snowflakes (deduplicated). Channel `""` disables syncing; otherwise it must be in `GuildTextChannels` (400) |
| `DELETE /api/servers/{sid}` | admin | | 204 |
| `GET /api/servers/{sid}/discord/roles` | admin | | `Role[]` |
| `GET /api/servers/{sid}/discord/channels` | admin | | `Channel[]` |
| `GET /api/servers/{sid}/series` | access | | `Series[]` |
| `POST /api/servers/{sid}/series` | access | `{title, creator?, url?, default_venue?, notes?}` | 201 `Series` |
| `PATCH/DELETE /api/servers/{sid}/series/{id}` | access | `SeriesPatch` | `Series` / 204 |
| `GET /api/servers/{sid}/videos` | access | `?status=queued,reacted&venue=stream&series_id=3` | `Video[]` |
| `POST /api/servers/{sid}/videos` | access | `{title, venue, series_id?, episode?, url?, scheduled_for?, notes?}` | 201 `Video` |
| `GET/PATCH/DELETE /api/servers/{sid}/videos/{id}` | access | `VideoPatch` | `Video` / 204 |
| `POST /api/servers/{sid}/videos/reorder` | access | `{ids: [..]}` | 204 |
| `GET /api/servers/{sid}/suggestions` | access | `?status=pending&sort=newest\|oldest\|votes` | `Suggestion[]` |
| `POST /api/servers/{sid}/suggestions/{id}/approve` | access | same body as create video | `{suggestion, video}` |
| `POST /api/servers/{sid}/suggestions/{id}/discard` | access | | `Suggestion` |
| `POST /api/servers/{sid}/suggestions/{id}/restore` | access | | `Suggestion` |
| `POST /api/servers/{sid}/suggestions/sync` | access | | 204; 503 if sync disabled; 400 if the server has no channel |

Content validation is unchanged from before: title required (≤300 runes), venue/status
validity, venue defaults from the series, `series_id` must exist **in the same server** (400
"unknown series"), approve of non-pending gives 409, discard/restore of approved gives 409, and status
moving to reacted/patreon/youtube auto-sets the matching timestamp if unset.

`GET /api/health` is public and handled in main.

## Frontend (`web/`)

Vite + React + TS, `react-router` v7. Dev: Vite on :5173 (all interfaces) proxies `/api` to :8080.

- `/login`: "Sign in with Discord" link to `/api/auth/login`; messages for `?error=discord|cancelled`.
- On load `GET /api/me` (401 sends you to `/login`), then `GET /api/servers`.
- Routes are scoped by server: `/s/:serverId` (Queue), `/s/:serverId/pipeline`, `/s/:serverId/series`,
  `/s/:serverId/series/:id`, `/s/:serverId/suggestions`. `/` redirects to the last-used server
  (localStorage, guarded with try/catch) or the first one. With no servers: admins see a prompt to
  add one, others see "You don't have access to any servers yet; ask an admin to add your mod role".
- Header: **server switcher** (guild icon `https://cdn.discordapp.com/icons/{guild_id}/{icon}.png?size=64`
  or initials + name), nav links for the current server, user avatar/name, Logout. Admins also get an
  "Admin" link.
- `/admin` (admin only): registered servers list, each linking to settings; "Add server" lists
  the bot's guilds that aren't registered yet (add with an optional display name) plus the bot invite
  link for servers not listed.
- `/admin/servers/:serverId`: settings. Display name; **mod roles** multi-select (checkbox list from
  `/discord/roles` with role colors); allowed user IDs (one per line or comma-separated); suggestions channel
  select (from `/discord/channels`, plus "Disabled"); Save; Delete server (confirm by typing its name;
  warn that all its data is deleted).
- Queue/Pipeline/Series/Suggestions pages behave as before but use the server-scoped endpoints.
  Sync now is hidden when the server has no suggestions channel.

Keep it dependency-light (no UI kit); plain CSS that works in light and dark mode.

## Self-registration (supersedes the sections above where they conflict)

Streamers can register their own servers. A **manager** of a Discord server is its owner or anyone
whose `@everyone` + role permissions include Administrator (`1<<3`) or Manage Server (`1<<5`):
`discord.CanManage(guild, member, userID)` (done, in `internal/discord/guilds.go`).
`SELF_REGISTRATION` (default `true`, `config.SelfRegistration`) enables it. When false, only app admins
register.

Permissions on a registered server:

| | Content (queue, series, suggestions) | Manage (settings, roles/channels lists, delete) |
|---|---|---|
| App admin | yes | yes |
| Server manager (live check) | yes | yes |
| Mod role / allowed user | yes | no |

### Store

Migration `003_self_registration.sql` (done): `server_access.can_manage`, table `user_guilds`.
Types `store.AccessEntry`, `store.UserGuild` (done, models.go). Method changes:

```go
GetAccess(ctx, serverID int64, userID string) (AccessEntry, bool /*found*/, error)
SetAccess(ctx, serverID int64, userID string, e AccessEntry) error
ReplaceUserGuilds(ctx, userID string, gs []UserGuild, now time.Time) error // tx: delete user's rows, insert gs
ListUserGuilds(ctx, userID string) ([]UserGuild, error)                    // ordered by name (case-insensitive)
```

### Access

```go
type Store interface { GetAccess(...); SetAccess(...) }        // new signatures above
type Discord interface {
    GuildMember(ctx, guildID, userID string) (discord.Member, error)
    Guild(ctx, guildID string) (discord.Guild, error)          // includes OwnerID and Roles with Permissions
}
type Perms struct { Access, Manage bool }
func New(st Store, d Discord, adminIDs []string, ttl time.Duration, log *slog.Logger) *Checker
func (c *Checker) IsAdmin(userID string) bool
func (c *Checker) Check(ctx, userID string, srv store.Server) (Perms, error)       // cached
func (c *Checker) CanManageGuild(ctx, userID, guildID string) (bool, error)        // live, uncached
```

`Check`:
1. Admin gives `{true, true}` with no fetch.
2. A cache entry younger than ttl gives its values.
3. Otherwise fetch the member and guild. Not a member gives `{Access: allowlisted, Manage: false}`.
   Otherwise `manage = discord.CanManage(g, m, user)` and
   `access = allowlisted || manage || any member role in srv.ModRoleIDs`. Cache it (AccessEntry with now).
4. If the fetch fails: a cache entry younger than 24h is used (logged); else an allowlisted user gets
   `{true, false}` with no error; else return the error.

Keep the existing coalescing of concurrent fetches per (server, user), with the fetch detached from
the caller's cancellation (fetchTimeout). Guild objects may be cached in memory per guild ID for ttl.

`CanManageGuild`: admin is true; member ErrNotFound or guild ErrNotFound is false; else `discord.CanManage`.
Other errors are returned.

### Auth

Scope becomes `identify guilds`. After `UpsertUser` in the callback, fetch `GET /users/@me/guilds`
with the user's bearer token (`limit=200`, page with `after` until fewer than 200), map each to
`UserGuild{GuildID, Name, Icon, CanManage: owner || discord.HasManagePermission(permissions)}` and
call `ReplaceUserGuilds`. A failure here is logged as a warning and doesn't block login. The snapshot only
drives the "servers you could add" list; every permission decision is live (Access).

### REST API changes

`Deps` gains `SelfRegistration bool`. The `Access` interface becomes `{IsAdmin, Check, CanManageGuild}`.
The `Bot` interface is unchanged. The `Store` interface adds `ListUserGuilds`.

- `{sid}` content routes need `Check().Access` (404 otherwise, 502 on error).
- **Manage routes** (`PATCH`/`DELETE /api/servers/{sid}`, `GET .../discord/roles`, `.../discord/channels`)
  need `Check().Manage`. No access gives 404; access without manage gives 403 `"you can't manage this server"`.
- `GET /api/me` gives `{user, is_admin, can_register}` where `can_register = is_admin || SelfRegistration`.
- `GET /api/servers` and `GET /api/servers/{sid}` return `ServerView`: the Server fields plus
  `can_manage` (embed `store.Server` so JSON is flat).
- `GET /api/admin/guilds` is **replaced by `GET /api/guilds`** (403 unless can_register):
  `{invite_url, guilds: [{id, name, icon, server_id: int|null, bot_present: bool, invite_url: string}]}`.
  - Admins get the bot's guilds plus their own managed guilds from the snapshot.
  - Others get only their managed snapshot guilds (`ListUserGuilds` where CanManage).
  - `bot_present` means the guild is in `BotGuilds`. A per-guild `invite_url` (only when `!bot_present`, else "") is
    the base invite URL plus `&guild_id={id}&disable_guild_select=true`.
  - Deduplicated by ID, sorted by name (case-insensitive).
- `POST /api/servers`: validate `guild_id`; `Bot.Guild` gives 400 "bot is not in that server" on ErrNotFound;
  then the permission check: admin, or `SelfRegistration && CanManageGuild` (live), otherwise 403
  `"you need Manage Server on that Discord server to add it"`. 502 on errors, 409 if already registered.
  The response is a `ServerView` with `can_manage: true`.

### Frontend changes

- Types: `Me.can_register`, `Server.can_manage`; `/api/admin/guilds` is replaced by `/api/guilds` (new shape).
- `/admin` becomes **`/manage`** ("Manage servers"; keep `/admin` and `/admin/servers/:id` as redirects).
  Shown in the header when `me.can_register` or any server has `can_manage`.
  - **Your servers**: servers with `can_manage`, linking to settings.
  - **Add a server** (when `can_register`): `/api/guilds` entries with `server_id === null`. If `bot_present`,
    show a name input + Add. Otherwise show "Invite the bot" (per-guild `invite_url`, new tab) and "then refresh".
    Hint: "Only servers where you have Manage Server are listed. Just got the permission? Sign out and in again."
    Admins also see the general invite link.
  - A 403 from Add shows the API message.
- Settings at `/manage/servers/:serverId`. If the server lacks `can_manage`, show "You can't manage this server".
- Empty state for non-admins: "You don't have access to any servers yet." plus "Ask a server admin
  to add your mod role", and when `can_register`, a button "Add your server" linking to `/manage`.

## YouTube integration (supersedes earlier sections where they conflict)

Optional `YOUTUBE_API_KEY` (`config.YouTubeAPIKey`, YouTube Data API v3). Without it, lookups/imports
are unavailable (503) but YouTube video IDs and thumbnails still work, because thumbnails are
derived from the ID: `youtube.ThumbnailURL(id)` = `https://i.ytimg.com/vi/{id}/mqdefault.jpg`.
**Thumbnail URLs are never accepted from clients or stored.** Only IDs, which the server extracts.

### `internal/youtube` (stubs in youtube.go)

- `ParseVideoID(s)`: trims space. Accepts a bare 11-char ID matching `^[A-Za-z0-9_-]{11}$`, or an
  http(s) URL whose host is `youtube.com`, `www.`/`m.`/`music.youtube.com` or `youtu.be`:
  - `youtu.be/{id}`
  - `/watch?v={id}`
  - `/shorts/{id}`
  - `/embed/{id}`
  - `/live/{id}`
  - `/v/{id}`

  The ID must match the regex. Anything else gives false.
- `ParsePlaylistID(s)`: a bare ID matching `^[A-Za-z0-9_-]{2,64}$` that starts with `PL`, `UU`, `OL`,
  `FL`, `RD` or `LL`; or a URL on the same hosts with a `list` query param matching `^[A-Za-z0-9_-]{2,64}$`.
- Client (`key` query param on every request; base URL overridable in tests via the unexported field):
  - `Videos`: `GET /videos?part=snippet,contentDetails&id=a,b,..&maxResults=50`, in batches of 50,
    returned in input order, missing IDs omitted. Duration is ISO 8601 (`PT1H2M3S`, `P1DT2H`,
    `PT45S`; `P0D` for live gives 0), parsed to seconds.
  - `Video`: `Videos([id])`, with ErrNotFound if empty.
  - `Playlist`: `GET /playlists?part=snippet&id=..`, with ErrNotFound if no items. Then
    `GET /playlistItems?part=snippet,contentDetails&playlistId=..&maxResults=50&pageToken=..`, paging until
    there's no `nextPageToken` or MaxPlaylistItems entries have been read.
    - Position = `snippet.position + 1`.
    - Skip entries whose title is "Deleted video" / "Private video" or that have no
      `contentDetails.videoId`.
    - Then `Videos` on the IDs for durations and channel. Items `Videos` doesn't return are dropped.
    - The result is ordered by position.
  - Errors: non-2xx gives an error with status and a truncated body (never include the key; strip it
    from any URL in error messages). 404 gives ErrNotFound. HTTP client timeout 15s.

### Store

Migration `004_youtube.sql` (done). New fields (done in models.go):
- `Video`: `YouTubeVideoID`, `ChannelTitle`, `DurationSeconds`.
- `VideoPatch`: `YouTubeVideoID` (json "-"), `ChannelTitle`, `DurationSeconds`.
- `Series`: `YouTubePlaylistID`, `ThumbnailVideoID`.
- `SeriesPatch`: `YouTubePlaylistID`, `ThumbnailVideoID` (both json "-").

All create/update/scan paths handle them. New methods:

```go
// CreateVideos inserts videos (all must share one non-zero ServerID) in one transaction,
// appending positions in input order after the server's current max.
CreateVideos(ctx, vs []Video) ([]Video, error)
// CreateSeriesWithVideos creates the series, then the videos with SeriesID set to it, in one transaction.
CreateSeriesWithVideos(ctx, s Series, vs []Video) (Series, []Video, error)
```

### REST API

`Deps` gains `YouTube YouTube` (interface: `Video`, `Playlist`; nil when not configured).
`/api/me` adds `youtube_enabled: bool`.

- On `POST /videos`, `PATCH /videos/{id}` (when `url` is present) and approve, the server sets
  `youtube_video_id = ParseVideoID(url)` (or "" if it isn't a YouTube URL). `channel_title` (≤200 runes) and
  `duration_seconds` (0..1e7) are accepted from the body.
- On `POST /series` and `PATCH /series/{id}` (when `url` is present), `youtube_playlist_id = ParsePlaylistID(url)` or "".
- `GET /api/youtube/video?url=..` (any signed-in user): 400 "not a YouTube video URL", 503
  "YouTube lookups are not configured", 404, 502. Returns `youtube.Video`.
- `GET /api/youtube/playlist?url=..`: same errors; returns `{playlist, items}`.
- `POST /api/servers/{sid}/series/import` (access): `{playlist_url, title?, default_venue?, video_ids?: []}`.
  - The server fetches the playlist itself; `video_ids` (if present) filters it and must be a subset (else 400).
  - It creates the series:
    - `title` = body title or the playlist title (validated ≤300);
    - `creator` = playlist channel;
    - `url` = playlist URL;
    - `youtube_playlist_id`;
    - `thumbnail_video_id` = first imported item;
    - `default_venue` = body value or watch_party.
  - It creates the videos with `CreateSeriesWithVideos`:
    - status queued and venue = series default;
    - `episode` = `strconv.Itoa(Position)`;
    - title, `url` = video URL, `youtube_video_id`, `channel_title`, `duration_seconds`;
    - `created_by` = me.
  - 201 `{series, videos}`. 400 if nothing would be imported.
- `POST /api/servers/{sid}/series/{id}/import` (access): `{playlist_url?, video_ids?}`.
  `playlist_url` defaults to the series' playlist (400 if neither is given). It adds playlist items whose
  `youtube_video_id` isn't already in this series (via `ListVideos{SeriesID}`), filtered by `video_ids`,
  with `CreateVideos`. It sets the series' `youtube_playlist_id` if it was empty. 200 `{series, videos}` (videos = added only; may be []).
- All YouTube upstream errors are logged; the client gets a generic message.

### Frontend

- `youtubeThumb(id)` helper (same URL as the server). Thumbnails come only from `youtube_video_id`
  / `thumbnail_video_id` (lazy-loaded `<img>`, alt text = title). Duration is formatted `h:mm:ss` / `m:ss`.
- VideoForm: when `me.youtube_enabled` and the URL is YouTube, show a "Fetch details" button.
  It auto-runs once when a YouTube URL is pasted/blurred and the title is empty.
  It fills title (if empty, else asks before replacing), channel and duration, and shows a thumbnail preview.
  It sends `channel_title` and `duration_seconds`.
- Video cards (Queue, Pipeline, SeriesDetail) show the thumbnail and a duration badge. Suggestion
  cards show a thumbnail for the first YouTube URL in the content (ID parsed client-side with the same rules).
- SeriesList "New series" gains a "From YouTube playlist" mode (when `youtube_enabled`):
  1. Paste a URL and Load preview: playlist title/channel and items as rows with checkbox, position, thumbnail, title, duration;
     all checked, with select all/none.
  2. Edit title and venue.
  3. Import (sends `video_ids` of the checked items). Then go to the new series.
- SeriesDetail: a series header thumbnail. When `youtube_playlist_id` is set and youtube is enabled,
  "Check playlist for new videos" previews items not yet in the series (compare
  `youtube_video_id`) and imports the checked ones.

## Edited file link and link validation

- `videos.edit_url` (migration 005): link to the edited video file (e.g. Drive/Dropbox) for the editing
  stage. It is on `Video`/`VideoPatch` and shown on the Pipeline card between VOD and Patreon.
- Every user-supplied link (`url` on videos/series, `vod_url`, `edit_url`, `patreon_url`, `youtube_url`)
  is trimmed and must be empty or an `http(s)` URL with a host, at most 2000 chars (`validLink` in api).
  Otherwise the response is 400 "`<field>` must be an http(s) URL". Links are rendered as hrefs for other mods.

## Suggestion video details

When `YOUTUBE_API_KEY` is set, each suggestion gets the details of the **first YouTube video link** in its
message, so mods see the real video title instead of only the raw message.

### Store

Migration `006_suggestion_video.sql` (done). Fields on `Suggestion` (done): `YouTubeVideoID`, `VideoTitle`,
`ChannelTitle`, `DurationSeconds` (JSON) and `VideoLookupAt` (json "-"; nil = not looked up). Type
`store.SuggestionVideo` (done).

- Scan the new columns everywhere suggestions are read.
- `UpsertSuggestion` on conflict: if `content` changed, also reset `youtube_video_id`, `video_title`,
  `channel_title` to '', `duration_seconds` to 0 and `video_lookup_at` to NULL. The scoping guard
  (`suggestions.server_id = excluded.server_id`) stays.
- New methods:

```go
// PendingVideoLookups returns up to limit suggestions of the server with video_lookup_at IS NULL,
// oldest posted first.
PendingVideoLookups(ctx, serverID int64, limit int) ([]Suggestion, error)
// SetSuggestionVideo stores v and sets video_lookup_at = at, scoped by server (ErrNotFound otherwise).
SetSuggestionVideo(ctx, serverID, id int64, v SuggestionVideo, at time.Time) error
```

### `internal/suggest` (new package)

```go
type Store interface { PendingVideoLookups(...); SetSuggestionVideo(...) }
type Videos interface { Videos(ctx, ids []string) ([]youtube.Video, error) } // *youtube.Client
func NewEnricher(st Store, yt Videos, log *slog.Logger) *Enricher
// Enrich looks up pending suggestions of one server (up to 200 per call).
func (e *Enricher) Enrich(ctx context.Context, serverID int64) error
// FirstVideoID returns the first YouTube video ID linked in Discord message text, or "".
func FirstVideoID(content string) string
```

- `FirstVideoID`: split on whitespace. For each token, strip Discord's embed-suppression `<…>`, spoiler `||`,
  and markdown link syntax `[text](url)` (use the url part), and trailing `)`, `.`, `,`, `!`, `?`, `;`, `:`, `>`.
  Return the first token for which `youtube.ParseVideoID` succeeds **and** which contains "://" or starts
  with "youtu" or "www." (a bare 11-letter word must not count as an ID).
- `Enrich`:
  1. Fetch pending.
  2. Suggestions without an ID are marked looked up with a zero `SuggestionVideo`.
  3. Unique IDs are looked up with one `Videos` call (it batches by 50).
  4. Found videos store ID, title, channel and duration. IDs not returned (deleted/private) store just the ID
     with an empty title, so the client can still show the thumbnail.
  5. If `Videos` errors, return the error **without marking anything**, so the next sync retries.
- main sets `poller.AfterSync` to call `Enrich` only when a YouTube key is configured, logging errors.

### Frontend

- Suggestion card: when `video_title` is set, show it as the card's heading (bold), with channel · duration
  under it, and the thumbnail from `youtube_video_id` (fall back to the existing client-side
  parsing when empty). The original Discord message, author, time and reactions stay below.
- Approve form prefill: title = `video_title` if set (else the existing guess), url = the YouTube
  watch URL for `youtube_video_id` if set (else the first URL), `channel_title` and `duration_seconds` from the suggestion.

## Suggesters and self-suggestions

Track who suggested each video, and optionally auto-discard **self-suggestions**: a suggestion is a
self-suggestion when the suggester's Discord display name or username equals the linked video's YouTube
channel title or @handle after normalization. `suggest.IsSelfSuggestion(discordNames, channelNames)` is
done (internal/suggest/self.go), along with its tests. Detection needs `YOUTUBE_API_KEY`.

Migration `007_suggesters.sql` (done; it also resets `video_lookup_at` so existing rows are re-checked).
Model fields (done, models.go):
- `Server.BlockSelfSuggestions` and `ServerPatch.BlockSelfSuggestions`.
- `Video.SuggestedByID` and `Video.SuggestedByName`.
- `Suggestion.AuthorUsername`, `ChannelHandle`, `SelfSuggestion`, `DiscardReason` (json) and `SelfOverride` (json "-").
- `SuggestionVideo.ChannelHandle` and `SuggestionVideo.SelfSuggestion`.
- `SuggestionFilter.AuthorID`.
- `store.DiscardSelfSuggestion` = "self_suggestion".
- `youtube.Video.ChannelID` and the `(*youtube.Client).ChannelHandles` stub.

### youtube

- `Videos` also fills `ChannelID` from `snippet.channelId`.
- `ChannelHandles(ctx, ids)`: `GET /channels?part=snippet&id=a,b..&maxResults=50`, in batches of 50, deduplicating IDs,
  mapping `id` to `snippet.customUrl`. Ensure a leading "@"; omit empty ones. Same error and key-redaction rules as
  other calls. No IDs means no request.

### Store

- **Servers:** read/write `block_self_suggestions` in every server path, including `UpdateServer` via the patch.
- **`UpsertSuggestion`:** insert `author_username`. On conflict also update `author_username`. The content-change reset
  also clears `channel_handle` and `self_suggestion` (but not `discard_reason`/`self_override`/status).
- **`SetSuggestionVideo`:** also stores `channel_handle` and `self_suggestion`.
- **`SetSuggestionStatus`:** `discarded` or `approved` sets `discard_reason = ''`. `pending` (restore) sets `self_override = 1`
  when the current `discard_reason` is `self_suggestion`, then `discard_reason = ''`.
- **`ApproveSuggestion`:** the created video gets `suggested_by_id = author_id` and `suggested_by_name = author_name`, and
  `discard_reason = ''`.
- **`ListSuggestions`:** filters by `author_id` when `f.AuthorID != ""`.
- **Videos:** read/write `suggested_by_id`/`suggested_by_name` on create (from the Video struct) and in every read path.
  They are not patchable.
- **New method:**

```go
// DiscardSelfSuggestions marks the server's pending, self-flagged, non-overridden suggestions as
// discarded with discard_reason 'self_suggestion', reviewed_by '' and reviewed_at = now. Returns the count.
DiscardSelfSuggestions(ctx, serverID int64, now time.Time) (int64, error)
```

### suggest.Enricher

- The `Videos` interface becomes `{Videos(...); ChannelHandles(...)}`. The `Store` interface adds
  `GetServer(ctx, id) (store.Server, error)` and `DiscardSelfSuggestions`.
- `Enrich`, after the existing video lookup:
  1. Call `ChannelHandles` once for the found videos' channel IDs. On error, log a warning and continue without handles.
  2. For each found video, `SelfSuggestion = IsSelfSuggestion([author_name, author_username], [channel_title, handle])`,
     and `ChannelHandle` is stored.
  3. Finally, always (even with nothing pending): `GetServer`, and if `BlockSelfSuggestions`, `DiscardSelfSuggestions`.
     That makes enabling the setting apply to existing pending suggestions on the next sync.

### discord poller

`toSuggestion` sets `AuthorUsername = author.username` (`AuthorName` stays global_name, else username).

### REST API

- **Server patch:** `PATCH /api/servers/{sid}` accepts `block_self_suggestions` (manage permission, like the other settings).
- **Suggestions filter:** `GET /api/servers/{sid}/suggestions?author_id=<snowflake>` filters by suggester (400 if not a snowflake).
- **Fields:** suggestions and videos expose the new fields automatically (JSON tags).

### Frontend

- **Suggestion card:**
  - The author name is a button that filters the page to that suggester (`?author=<id>`, kept with the existing
    status/sort params). While filtered, a header says "Suggestions by <name>" with a clear (×) button.
  - When `self_suggestion`, show a warning badge "Self-promo?" with a tooltip "The suggester's Discord name matches
    the YouTube channel".
  - When `discard_reason === 'self_suggestion'`, the Discarded tab shows "Auto-discarded: self-suggestion".
    Restore works as before.
  - Show the channel @handle next to the channel title when present.
- **Videos:** Queue cards, the Pipeline details panel and SeriesDetail rows show "Suggested by <name>" when
  `suggested_by_name` is set.
- **Server settings:** a checkbox "Automatically discard self-suggestions", with help text "When a suggester's Discord
  name matches the YouTube channel of the video they suggest. Requires YouTube lookups. You can still restore them."
  It is saved with the other settings.

## Video editor assignment

The Pipeline details panel has an **Editor** dropdown. It lists app users with access to the server, plus
"Other…" (a free-text name for editors who don't use the app) and "Unassigned".

Migration `008_editor.sql` (done). Fields (done): `Video.EditorID`, `Video.EditorName`, `VideoPatch.EditorID`,
`VideoPatch.EditorName`.

### Store

- **Video paths:** read/write `editor_id`/`editor_name` in video create, read and patch paths (patch via setPtr, like the
  other fields).
- **`ListUsers(ctx) ([]User, error)`:** all users, ordered by display name (global_name, else username,
  case-insensitive), then discord_id. An empty non-nil slice when there are none.
- **`GetUser(ctx, discordID string) (User, error)`:** returns ErrNotFound when missing.

### REST API

- **`GET /api/servers/{sid}/members` (access):** `[{discord_id, name, avatar}]` for every user from `ListUsers` for whom
  `Access.Check(ctx, user.DiscordID, srv).Access` is true. `name` is global_name, else username. A user
  whose check errors is skipped (logged). Cap at the first 500 users.
- **`PATCH /api/servers/{sid}/videos/{id}`** accepts `editor_id` and `editor_name`:
  - `editor_id` non-empty: it must be a snowflake, `GetUser` must find it (400 "unknown editor"), and
    `Check(...).Access` must be true (400 "that user can't access this server"; 502 on a check error).
    The server then sets `editor_name` from that user's display name, ignoring any client `editor_name`.
  - `editor_id` == "" (present): `editor_name` is trimmed, ≤100 runes (400 "editor_name is too long").
    Both "" means unassigned.
  - `editor_name` present without `editor_id`: treated as a free-text editor, so `editor_id` is set to "" too.
  - Neither present: untouched.

### Frontend

- **`api.ts`:** `editor_id`/`editor_name` on Video and VideoPatch; `listMembers(sid)`.
- **VideoDetails (Pipeline panel):** an "Editor" select with options "Unassigned", each member (by name; "(you)" after the
  current user), and "Other…".
  - Choosing a member or Unassigned saves immediately (PATCH).
  - Choosing "Other…" shows a text input plus Save.
  - A free-text editor shows as "Other…" with the name in the input.
  - An editor_id that is no longer in the member list still shows as its stored name.
  - Errors appear inline.
- **Kanban card:** a small muted "✎ <editor_name>" line when set. Queue cards don't need it.

## Removing suggestions

Mods can **remove** suggestions to clean up the Suggestions page. Removal is a soft delete: rows stay (with
`removed_at` set) because the poller re-reads the latest 100 Discord messages each sync, so a hard-deleted row
would be re-imported. Removed rows never appear in any tab and are never re-imported or re-checked.

Migration `009_remove_suggestions.sql` (done). Field `Suggestion.RemovedAt` (json "-", done).

### Store

- `ListSuggestions`, `PendingVideoLookups` and `DiscardSelfSuggestions` ignore rows with `removed_at IS NOT NULL`.
- `GetSuggestion` still returns removed rows (needed for undo).
- `UpsertSuggestion`'s conflict update never touches `removed_at`, so a removed row stays removed.
- New methods:

```go
// RemoveSuggestions sets removed_at = now on the given ids of the server that aren't removed yet,
// in one statement; ids from other servers are ignored. Returns the number of rows changed.
RemoveSuggestions(ctx, serverID int64, ids []int64, now time.Time) (int64, error)
// UnremoveSuggestion clears removed_at (ErrNotFound if the id isn't in the server).
UnremoveSuggestion(ctx, serverID, id int64) error
```

### REST API (access; mods can do it)

- `POST /api/servers/{sid}/suggestions/remove` with body `{ids: [..]}`. `ids` must be non-empty, ≤ 500, and positive
  integers (deduplicated); otherwise 400. Returns 200 `{removed: n}`.
- `POST /api/servers/{sid}/suggestions/{id}/unremove` returns 204, or 404.
- Approving, discarding or restoring a removed suggestion gives 404 "suggestion not found".

### Frontend

- **Remove button:** every suggestion card (all tabs) has a "Remove" button.
  - It calls remove with `[id]`, removes the card from the list immediately, and shows a notice
    "Removed. **Undo**" (Undo calls unremove, then refetches).
  - Only the most recent removal is undoable, and the notice clears on the next action or tab change.
- **Remove all:** on the Approved and Discarded tabs (only), a "Remove all N" button in the list header removes
  every suggestion currently shown (respecting the author filter). It first asks for confirmation:
  "Remove N suggestions from this list? They won't come back on sync. Approved videos stay in the queue."
  Afterwards it shows "Removed N suggestions." There is no undo for bulk removal.

## Optional app admins

`APP_ADMIN_USER_IDS` is optional. It's required only when `SELF_REGISTRATION=false` (otherwise nobody could
register servers). main logs a warning when it's empty. Without admins, each server is managed only by
people with Manage Server on that Discord server.

## Videos suggested by several people

When several people suggest the same YouTube video, each of those suggestion cards shows how many people
suggested it and who. Two suggestions match when they have the same non-empty `youtube_video_id`, so this needs
`YOUTUBE_API_KEY`. Every suggestion of the server counts, in any status, except removed ones. `store.Suggester` is
done (models.go).

### Store

```go
// SuggestersByVideo returns, for each of the given YouTube video IDs (empty ones ignored, duplicates
// collapsed), the distinct authors of the server's non-removed suggestions linking it, in any status,
// ordered by their earliest suggestion (FirstAt). Video IDs with no suggestions are absent from the map.
// Query in chunks of at most 500 IDs. AuthorName is the name on that author's most recent suggestion of the video.
SuggestersByVideo(ctx, serverID int64, videoIDs []string) (map[string][]Suggester, error)
```

### REST API

`GET /api/servers/{sid}/suggestions` returns `SuggestionView` items: the embedded `store.Suggestion` (flat JSON)
plus `suggesters: Suggester[]`. Load the list, then call `SuggestersByVideo` once for the listed suggestions'
video IDs. `suggesters` is the map entry for the suggestion's video, or `[]` when it has none. Keep the existing
filters and sorting unchanged. Other routes are unchanged.

### Frontend

- `api.ts`: `Suggester` type; `Suggestion.suggesters: Suggester[]` (on list results).
- **Suggestion card**, when `suggesters.length > 1`:
  - a badge "×N" (title "Suggested by N people") next to the title;
  - a muted line "Also suggested by A, B, C": everyone except this card's author, in order, each name a
    link-style button that applies the existing author filter (`?author=<id>`).
  - With more than 5 others, show the first 5 and "+K more", which expands the list on click.

## Clips

Twitch clips shared in a per-server Discord **clips channel** are pulled in like suggestions. They land in a
**clips inbox** (status `new`). Mods **select** (move to `pending`) or **discard** them. Selected clips live on a
**clip board** with the columns Pending → Edited → Posted, each with the edited video's title/link and the links of
the posts on YouTube, Instagram, X and TikTok. The Posted column can be filtered to clips not yet on some platforms.
Clips are looked up in the **Twitch Helix API** (title, who clipped it, broadcaster, thumbnail, length) when
`TWITCH_CLIENT_ID` and `TWITCH_CLIENT_SECRET` are set (app access token; optional).

Done: migration `010_clips.sql`; models (`store.Clip`, `ClipPatch`, `ClipStatus`, `Platform`, `ClipFilter`, `ClipSort`,
`ClipTwitch`, `Server.ClipsChannelID/ClipsCursor`, `ServerPatch.ClipsChannelID`); `internal/twitch/parse.go`
(`ParseClipSlug`, `FirstClip`, tested); the `internal/twitch/client.go` types plus the `Clips` stub; config
`TwitchClientID/TwitchClientSecret`; and the web `api.ts` clip types/functions (`listClips`, `getClip`, `updateClip`,
`syncClips`, `PLATFORMS`).

### Store (`internal/store`)

Every query is scoped by `server_id`.

- **Servers:** read/write `clips_channel_id` everywhere. Changing it to a different value resets `clips_cursor` to ''.
  New `SetClipsCursor(ctx, serverID, cursor) error`.
- **`UpsertClip(ctx, c Clip) error`:**
  - On insert, status is `new`, `status_at` = now, created/updated = now.
  - On conflict(`discord_message_id`), and only when `clips.server_id = excluded.server_id`, update `author_name`,
    `author_username` and `content`. If `content` changed **and** status is still `new`, also set `clip_url`,
    `twitch_clip_id`, `title` from the new values, and reset `clipper_name`, `broadcaster_name`, `thumbnail_url`,
    `duration_seconds`, `clip_created_at` and `twitch_lookup_at` (to ''/0/NULL).
- **`ListClips(ctx, f ClipFilter) ([]Clip, error)`:**
  - Filters by statuses and by `Missing` (keep rows where any listed platform's url column is '').
  - Sorting:
    - `newest` (default): `posted_at DESC`.
    - `oldest`: `posted_at ASC`.
    - `status_at`: `status_at ASC`.
    - `id` breaks ties in all three.
  - An empty non-nil slice when nothing matches.
- **`GetClip(ctx, serverID, id) (Clip, error)`:** returns ErrNotFound when missing.
- **`UpdateClip(ctx, serverID, id int64, p ClipPatch, reviewerID string, now time.Time) (Clip, error)`:**
  - Applies the non-nil fields.
  - A status change sets `status_at` = now.
  - Moving from `new`/`discarded` to `pending`/`edited`/`posted` while `reviewed_at` is NULL sets `reviewed_by` and
    `reviewed_at`.
  - Entering `posted` with `first_posted_at` NULL sets it to now.
  - Always bumps `updated_at`.
- **`PendingClipLookups(ctx, serverID, limit) ([]Clip, error)`:** rows with `twitch_clip_id != ''` and
  `twitch_lookup_at IS NULL`, oldest `posted_at` first.
- **`SetClipTwitch(ctx, serverID, id int64, t ClipTwitch, at time.Time) error`:** sets clipper/broadcaster/thumbnail/
  duration/`clip_created_at` and `twitch_lookup_at` = at. It sets `title` = `t.Title` only when that is non-empty.

### Twitch (`internal/twitch/client.go`)

- **Token:** POST `{authBase}/token` with form `client_id`, `client_secret`, `grant_type=client_credentials`, giving
  `{access_token, expires_in}`. Cache it under the mutex and refresh when it's within 60s of expiry. On a 401 from
  Helix, drop the token and retry the request once.
- **`Clips`:** `GET {apiBase}/clips?id=a&id=b…` (≤100 ids per request) with headers `Client-Id` and
  `Authorization: Bearer <token>`. Results are in input order; missing ones are omitted; no request for no slugs.
- **Errors:** non-2xx gives an error with the status and a truncated body, and must **never** include the client
  secret or token. 429 waits for the `Ratelimit-Reset` (unix seconds) header, capped at 10s, and retries once.

### Clip enricher (`internal/clips`, new package)

- `NewEnricher(st Store, tw Twitch, log)` and `Enrich(ctx, serverID) error`.
- `Store` = `{PendingClipLookups, SetClipTwitch}`; `Twitch` = `{Clips}`.
- Enrich looks up ≤200 pending clips with one `Clips` call:
  - **Found:** `ClipTwitch{Title, ClipperName: CreatorName, BroadcasterName, ThumbnailURL, DurationSeconds:
    round(Duration), CreatedAt}`. Keep `ThumbnailURL` only if it is https with a host ending in `.jtvnw.net`.
  - **Not found:** zero `ClipTwitch` (marked looked up, not retried).
  - **Clips error:** mark nothing and return the error.
- main wires it into `poller.AfterSync` only when the Twitch credentials are set.

### Discord poller (`internal/discord`)

- `SyncServer` syncs the suggestions channel (if set) and then the clips channel (if set). Each has its own cursor
  (`SuggestionsCursor` / `ClipsCursor`, saved via `SetSuggestionsCursor` / `SetClipsCursor`), its own
  channel-changed check, and the same paging, content-missing guard and recent-100 refresh.
- It attempts both even if one fails and returns `errors.Join` of the failures. `AfterSync` runs if at least one
  configured channel synced OK. `Run` syncs servers that have either channel.
- **Clip parsing** (`toClip`), with the same author/type filters as suggestions:
  - `clip_url, slug = twitch.FirstClip(content)`; else the first http(s) URL token in the content; else the first
    attachment URL; else **skip the message** (not a clip).
  - `title` = the content with URL tokens removed, whitespace collapsed, trimmed, ≤300 runes.
  - `message_url = https://discord.com/channels/{srv.GuildID}/{channelID}/{messageID}`.
  - `AuthorName` = global_name else username; `AuthorUsername` = username.
- The store interface adds `UpsertClip` and `SetClipsCursor`.

### REST API (access unless noted)

- **`/api/me`:** adds `twitch_enabled` (`Deps.TwitchEnabled`).
- **`GET /api/servers/{sid}/clips?status=a,b&sort=newest|oldest|status_at&missing=youtube,x`:** validates each
  value (400 otherwise).
- **`GET /api/servers/{sid}/clips/{id}`.**
- **`PATCH /api/servers/{sid}/clips/{id}`** with `ClipPatch`:
  - status must be valid;
  - `edited_url` and the 4 platform urls go through `validLink`;
  - `edited_title` is trimmed, ≤300;
  - `notes` ≤2000;
  - the reviewer is the current user.
- **`POST /api/servers/{sid}/clips/sync`:** 503 if `Sync` is nil; 400 "this server has no clips channel"; 502 on a sync
  error (using the Message Content message as with suggestions); else 204.
- **`PATCH /api/servers/{sid}` (manage):** `clips_channel_id` is validated like `suggestions_channel_id` ('' disables;
  else a snowflake listed in `GuildTextChannels`), plus 400 "use different channels for clips and suggestions" if
  it would equal the suggestions channel after the patch (and vice versa).

### Frontend

- **Navigation** (current server): add "Clips" (`/s/:sid/clips`, the inbox) and "Clip board" (`/s/:sid/clips/board`)
  after Suggestions.
- **Clips inbox** (`pages/Clips.tsx`):
  - Tabs New / Discarded, a sort select (newest/oldest), "Sync now" (hidden without a clips channel; a hint links
    managers to settings).
  - Each card shows:
    - the thumbnail (`thumbnail_url`), with a duration badge;
    - the title (fallback "Untitled clip"), linking to `clip_url`;
    - "Clipped by <clipper_name>" when known;
    - "shared by <author_name>" and the posted time;
    - the broadcaster;
    - a "Discord message" link.
  - Actions: **Select** (status `pending`) and **Discard** on New; **Restore** (status `new`) on Discarded.
    Optimistic removal from the list; errors show inline.
- **Server settings:** a "Clips channel" select like the suggestions channel ("Disabled" + text channels).
- **Clip board** (`pages/ClipBoard.tsx`), styled like the Pipeline kanban:
  - **Columns:** Pending, Edited, Posted, full width, with native drag-and-drop between them (same conventions as
    Pipeline, including the custom MIME type and resetting drag state on window dragend/drop) calling
    `updateClip(status)`.
  - **Cards:** thumbnail, edited_title || title, "clipped by", and on Posted cards small platform badges (filled
    when that platform's url is set).
  - **Details panel** on the right (`?clip=ID`, Escape/× closes, full screen under 800px), showing:
    - the clip link, clipped by / shared by, the clip title, the Discord message link;
    - a status select (also Discarded/New, to send it back);
    - inputs for edited title, edited video link, YouTube, Instagram, X, TikTok (and notes), with Save sending only
      changed fields; links display as clickable when set.
  - **Posted column header:** filter chips "Not on: YouTube · Instagram · X · TikTok" (multi-select). When any are
    selected, pass `missing=` to `listClips` for the Posted column, so it only shows clips missing at least one
    selected platform. The selection is remembered per browser (localStorage with try/catch).

## Editor roles (section access)

Two per-server role lists restrict people to parts of a server:
- `clip_editor_role_ids`: **clip editors** may use only the clip pages (Clips inbox, Clip board).
- `video_editor_role_ids`: **video editors** may use only the video pages (Queue, Pipeline, Series).
- **Full access** (mod roles, allowed users, server managers, app admins) is everything, as before.
- Suggestions remain full-access only. Someone with both editor roles gets both sections.

Done: migration `011_editor_roles.sql` (resets the access cache); `Server.ClipEditorRoleIDs/VideoEditorRoleIDs`,
`ServerPatch.ClipEditorRoleIDs/VideoEditorRoleIDs`, `AccessEntry.CanVideos/CanClips` (models.go; `AccessEntry.Allowed`
now means *full* access).

### Store

- **Servers:** read/write both role lists (JSON arrays like `mod_role_ids`; nil is stored as `[]` and read back as a
  non-nil empty slice). `UpdateServer` deletes the server's `server_access` rows when either list is patched
  (like mod roles / allowed users).
- **Access cache:** `GetAccess`/`SetAccess` read/write `can_videos` and `can_clips`.

### Access (`internal/access`)

`Perms` becomes:

```go
type Perms struct {
    Access bool // any section: Full || Videos || Clips
    Full   bool // every section (mod / allowed user / manager / admin)
    Manage bool
    Videos bool // video pages: Full || video editor role
    Clips  bool // clip pages: Full || clip editor role
}
```

`Check`:
1. Admin gets everything.
2. Otherwise compute from the member fetch:
   - `manage` as today;
   - `full = allowlisted || manage || mod role`;
   - `videos = full || video editor role`;
   - `clips = full || clip editor role`;
   - `access = full || videos || clips`.
3. A non-member gets `Full = Access = Videos = Clips = allowlisted`.
4. Cache: `AccessEntry{Allowed: full, CanManage, CanVideos, CanClips}`.
5. The fallbacks (stale cache, allowlisted without cache) apply the same mapping. Allowlisted counts as full.

`CanManageGuild` is unchanged.

### REST API

`api.Perms` mirrors `access.Perms` (main's adapter maps it). `{sid}` routes declare the section they need:

| Section | Routes |
|---|---|
| any access | `GET /api/servers/{sid}` |
| videos | `/videos*`, `/series*` (incl. playlist import), `/members` |
| clips | `/clips*` |
| full | `/suggestions*` |
| manage | as today (`PATCH`/`DELETE` server, `discord/roles`, `discord/channels`) |

- No access at all gives 404 "server not found". Access but not that section gives 403 "you don't have access to
  this section".
- `GET /api/servers` lists servers with `Access`. `ServerView` adds `sections: {videos, clips, suggestions}`
  (suggestions = Full) next to `can_manage`.
- `/members` lists users with `Videos` (they're the possible editors). Editor assignment requires the target to
  have `Videos` (400 "that user can't access this server" otherwise).
- `PATCH /api/servers/{sid}` validates both role lists like `mod_role_ids` (snowflakes, deduplicated).

### Frontend

- `api.ts`: `Server.clip_editor_role_ids`, `video_editor_role_ids`, `sections: {videos, clips, suggestions}`;
  `ServerPatch` gets both role lists.
- **Header nav** shows only the sections the current server allows: Queue/Pipeline/Series need `videos`,
  Suggestions needs `suggestions`, Clips/Clip board need `clips`.
- **Routes:** visiting a section the server doesn't allow shows "You don't have access to this section" with a link
  to the first allowed section. `/s/:sid` (Queue) redirects to the first allowed section when videos isn't allowed
  (order: Queue, Suggestions, Clips). The server switcher keeps the section if allowed on the new server, else goes
  to its first allowed section.
- **Server settings:** two more role checkbox lists, "Clip editor roles" and "Video editor roles", with help text,
  reusing the mod role list component and saved with the other settings.
