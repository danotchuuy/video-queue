-- Streamers (Discord server managers) can register their own servers.

-- Cached "may this user manage (configure/delete) this server", checked via the bot
-- alongside access.
ALTER TABLE server_access ADD COLUMN can_manage INTEGER NOT NULL DEFAULT 0;

-- Clear entries cached before can_manage existed so they get re-checked.
DELETE FROM server_access;

-- Snapshot of the user's Discord servers taken at login (OAuth "guilds" scope).
-- Used only to list servers a user could register; permission checks are live.
CREATE TABLE user_guilds (
    user_id    TEXT NOT NULL REFERENCES users(discord_id) ON DELETE CASCADE,
    guild_id   TEXT NOT NULL,
    name       TEXT NOT NULL,
    icon       TEXT NOT NULL DEFAULT '',
    can_manage INTEGER NOT NULL, -- owner, or Administrator / Manage Server permission
    updated_at TEXT NOT NULL,
    PRIMARY KEY (user_id, guild_id)
);
