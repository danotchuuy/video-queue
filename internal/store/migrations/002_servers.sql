-- Multiple Discord servers (one per streamer). All content is scoped to a server.

CREATE TABLE servers (
    id                     INTEGER PRIMARY KEY,
    guild_id               TEXT NOT NULL UNIQUE,
    name                   TEXT NOT NULL,             -- display name, e.g. the streamer's name
    guild_name             TEXT NOT NULL DEFAULT '',  -- Discord server name when registered
    icon                   TEXT NOT NULL DEFAULT '',  -- Discord guild icon hash
    mod_role_ids           TEXT NOT NULL DEFAULT '[]', -- JSON array of role IDs
    allowed_user_ids       TEXT NOT NULL DEFAULT '[]', -- JSON array of user IDs
    suggestions_channel_id TEXT NOT NULL DEFAULT '',
    suggestions_cursor     TEXT NOT NULL DEFAULT '',  -- last message ID synced from that channel
    created_by             TEXT NOT NULL DEFAULT '',
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL
);

-- Nullable only so existing rows survive the migration; the app always sets it,
-- and rows created before this migration are claimed by Store.ClaimUnassigned.
ALTER TABLE series      ADD COLUMN server_id INTEGER REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE videos      ADD COLUMN server_id INTEGER REFERENCES servers(id) ON DELETE CASCADE;
ALTER TABLE suggestions ADD COLUMN server_id INTEGER REFERENCES servers(id) ON DELETE CASCADE;

CREATE INDEX series_server      ON series(server_id);
CREATE INDEX videos_server      ON videos(server_id, status, position);
CREATE INDEX suggestions_server ON suggestions(server_id, status, posted_at);

-- Cached result of "may this user access this server" (role check via the bot).
CREATE TABLE server_access (
    server_id  INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL,
    allowed    INTEGER NOT NULL,
    checked_at TEXT NOT NULL,
    PRIMARY KEY (server_id, user_id)
);
