# Video Queue

A private web app for moderators of one or more VTubers to track reaction content.
Each streamer's Discord server is registered as a **server** in the app, and a server
switcher keeps everything in one place:

- **Queue** of videos to react to, split by venue: Discord **watch party** or **stream**.
- **Pipeline** for each reaction: reacted (recorded) → editing → posted to Patreon → posted to YouTube
  (or skipped).
- **Series**: anime abridged series and other shows, with their episodes.
- **Suggestions**: messages from each server's suggestions channel are pulled in automatically
  so mods can sort, approve (adds to the queue) or discard them.
- **Suggesters**: approved videos remember who suggested them, and the Suggestions page can be
  filtered by person. Optionally, self-suggestions (the suggester's Discord name matches the
  video's YouTube channel) are flagged or automatically discarded, per server.
- **Clips**: Twitch clips shared in each server's clips channel land in a clips inbox. Selected clips
  move onto a clip board (Pending → Edited → Posted) with the edited video's title/link and links
  for YouTube, Instagram, X and TikTok; the Posted column can show only clips not yet on a platform.
- **YouTube**: thumbnails and lengths on every YouTube video, "Fetch details" when adding a video,
  and series created from (and kept up to date with) a YouTube playlist.

Anyone can sign in with Discord. Who sees what:

| | Queue, series, suggestions | Server settings, delete |
|---|---|---|
| **Streamer**: owner, or Manage Server on that Discord server | yes | yes |
| **Mod**: one of the server's mod roles, or on its allowed list | yes | no |
| **App admin** (set in `.env`) | every server | every server |

Streamers register their own servers (unless `SELF_REGISTRATION=false`). Roles and permissions
are re-checked through the bot every 10 minutes. Data is stored in SQLite.

Go backend (`cmd/server`, `internal/`) + Vite/React frontend (`web/`). See [docs/design.md](docs/design.md)
for the data model and API.

## Discord setup (once)

1. Create an application at <https://discord.com/developers/applications>.
2. **OAuth2**: copy the Client ID and Client Secret. Under Redirects add
   `http://localhost:5173/api/auth/callback` (dev) and your production URL,
   e.g. `https://queue.example.com/api/auth/callback`.
3. **Bot**: reset the token and copy it, then enable the **Message Content Intent**.
4. Optional: to manage every server from one account, turn on Developer Mode in Discord
   (Settings → Advanced), right-click your own name → *Copy User ID*, and put it in `APP_ADMIN_USER_IDS`.

## Adding a streamer's server

The streamer (or anyone with Manage Server on her Discord server), or an app admin:

1. Sign in and open **Manage servers**. Servers you manage are listed under **Add a server**.
   If you only just got Manage Server, sign out and back in so the list updates.
2. Click **Invite the bot** next to the server and approve it in Discord. The bot needs *View Channels*
   and *Read Message History*, and must be able to see the suggestions channel.
3. Refresh, click **Add**, then on the settings page pick the **mod roles**, any extra allowed
   users, the **suggestions channel** and the **clips channel** (the bot must be able to see both).

## Configuration

Copy `.env.example` to `.env` and fill it in. Real environment variables override the file.

| Variable | Purpose |
|---|---|
| `DISCORD_CLIENT_ID`, `DISCORD_CLIENT_SECRET` | OAuth app credentials (required) |
| `DISCORD_REDIRECT_URL` | Must match a redirect registered in the app |
| `DISCORD_BOT_TOKEN` | Bot token (required) |
| `APP_ADMIN_USER_IDS` | Comma-separated Discord user IDs of app admins: they can add, configure and see every server. Optional, unless `SELF_REGISTRATION=false` |
| `SELF_REGISTRATION` | Let server owners/managers add their own servers (default `true`) |
| `TWITCH_CLIENT_ID`, `TWITCH_CLIENT_SECRET` | Optional Twitch app credentials for clip details (title, clipper, thumbnail) |
| `YOUTUBE_API_KEY` | Optional YouTube Data API v3 key for video details and playlist imports |
| `ACCESS_CACHE_TTL` | How long a mod-role check is trusted (default `10m`) |
| `SUGGESTIONS_POLL_INTERVAL` | How often to check suggestion channels (default `2m`) |
| `DATABASE_PATH` | SQLite file (default `video-queue.db`) |
| `ADDR`, `STATIC_DIR` | Listen address (`:8080`) and built frontend dir (`web/dist`) |
| `COOKIE_SECURE` | `true` when served over HTTPS |
| `SESSION_TTL` | Login lifetime (default `168h`) |

When a suggestions channel is first set, the whole channel is backfilled. After that only new
messages are fetched, and reaction counts on the latest 100 messages are refreshed each poll.

### YouTube API key (optional)

In the [Google Cloud console](https://console.cloud.google.com/), create a project, enable
**YouTube Data API v3** (APIs & Services → Library), then create an **API key** (Credentials).
Restricting the key to the YouTube Data API is recommended. The free quota (10,000 units/day)
is plenty: a video lookup costs 1 unit, and a 100-video playlist import costs about 6.

### Twitch app (optional)

Register an application at <https://dev.twitch.tv/console/apps> (any OAuth redirect URL, e.g.
`http://localhost`, category "Other"), then copy its Client ID and a new Client Secret into
`TWITCH_CLIENT_ID` / `TWITCH_CLIENT_SECRET`. The app uses an app access token only, so nobody
signs in with Twitch.

### Upgrading from the single-server version

If `.env` still has `DISCORD_GUILD_ID` (plus `DISCORD_MOD_ROLE_IDS`, `DISCORD_ALLOWED_USER_IDS`,
`DISCORD_SUGGESTIONS_CHANNEL_ID`), the server imports them on startup as a registered server and
attaches all existing data to it. Once it shows up under Manage servers, remove those four settings from `.env`.
While they're present, deleting that server in the app re-creates it on the next restart.

## Development

Requires Go 1.26+ and Node 24+.

```sh
cd web && npm install && cd ..
make dev-server   # terminal 1: Go API on :8080
make dev-web      # terminal 2: Vite on :5173, proxies /api to :8080
```

Open <http://localhost:5173>.

```sh
make test         # Go tests
cd web && npm run lint
```

## Production

```sh
make build        # builds web/dist and bin/server
./bin/server      # serves the API and the frontend on $ADDR
```

Run it behind HTTPS (e.g. Caddy or nginx), set `COOKIE_SECURE=true`, and set
`DISCORD_REDIRECT_URL` to the public callback URL. Back up the SQLite file
(`video-queue.db` plus its `-wal` file, or use `sqlite3 video-queue.db ".backup backup.db"`).
