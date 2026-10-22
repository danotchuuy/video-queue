-- Timestamps are TEXT in RFC 3339 (UTC, nanosecond precision). Discord IDs are TEXT snowflakes.

CREATE TABLE users (
    discord_id    TEXT PRIMARY KEY,
    username      TEXT NOT NULL,
    global_name   TEXT NOT NULL DEFAULT '',
    avatar        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    last_login_at TEXT NOT NULL
);

CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY, -- hex SHA-256 of the cookie token; raw tokens are never stored
    user_id    TEXT NOT NULL REFERENCES users(discord_id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX sessions_expires_at ON sessions(expires_at);

-- An abridged series or other multi-episode show she reacts to.
CREATE TABLE series (
    id            INTEGER PRIMARY KEY,
    title         TEXT NOT NULL,
    creator       TEXT NOT NULL DEFAULT '',
    url           TEXT NOT NULL DEFAULT '',
    default_venue TEXT NOT NULL DEFAULT 'watch_party' CHECK (default_venue IN ('watch_party', 'stream')),
    notes         TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

-- A single video/episode moving through: queued -> reacted -> editing -> patreon -> youtube.
-- 'skipped' means reacted to but not going to be edited/posted.
CREATE TABLE videos (
    id             INTEGER PRIMARY KEY,
    series_id      INTEGER REFERENCES series(id) ON DELETE SET NULL,
    episode        TEXT NOT NULL DEFAULT '', -- free-form: "12", "S2E4", "Movie"
    title          TEXT NOT NULL,
    url            TEXT NOT NULL DEFAULT '',
    venue          TEXT NOT NULL CHECK (venue IN ('watch_party', 'stream')),
    status         TEXT NOT NULL DEFAULT 'queued'
                   CHECK (status IN ('queued', 'reacted', 'editing', 'patreon', 'youtube', 'skipped')),
    position       INTEGER NOT NULL DEFAULT 0, -- queue order, ascending
    scheduled_for  TEXT,
    reacted_at     TEXT,
    vod_url        TEXT NOT NULL DEFAULT '', -- raw recording
    patreon_url    TEXT NOT NULL DEFAULT '',
    patreon_at     TEXT,
    youtube_url    TEXT NOT NULL DEFAULT '',
    youtube_at     TEXT,
    notes          TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL DEFAULT '', -- discord_id
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE INDEX videos_status_position ON videos(status, position);
CREATE INDEX videos_series ON videos(series_id);

-- Messages pulled from the Discord suggestions channel.
CREATE TABLE suggestions (
    id                 INTEGER PRIMARY KEY,
    discord_message_id TEXT NOT NULL UNIQUE,
    channel_id         TEXT NOT NULL,
    author_id          TEXT NOT NULL,
    author_name        TEXT NOT NULL,
    content            TEXT NOT NULL,
    reaction_count     INTEGER NOT NULL DEFAULT 0, -- total reactions; used as a rough vote count
    posted_at          TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'discarded')),
    reviewed_by        TEXT NOT NULL DEFAULT '',
    reviewed_at        TEXT,
    video_id           INTEGER REFERENCES videos(id) ON DELETE SET NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
CREATE INDEX suggestions_status_posted ON suggestions(status, posted_at);

-- Small key/value table, e.g. the last Discord message ID the poller has seen.
CREATE TABLE kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
