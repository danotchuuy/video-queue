-- Twitch clips shared in a per-server Discord channel: an inbox to review,
-- then a board (pending -> edited -> posted) with links per social platform.

ALTER TABLE servers ADD COLUMN clips_channel_id TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN clips_cursor     TEXT NOT NULL DEFAULT '';

CREATE TABLE clips (
    id                 INTEGER PRIMARY KEY,
    server_id          INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    discord_message_id TEXT NOT NULL UNIQUE,
    channel_id         TEXT NOT NULL,
    message_url        TEXT NOT NULL,             -- https://discord.com/channels/{guild}/{channel}/{message}
    author_id          TEXT NOT NULL,             -- Discord user who shared it
    author_name        TEXT NOT NULL,
    author_username    TEXT NOT NULL DEFAULT '',
    content            TEXT NOT NULL,             -- original message text
    posted_at          TEXT NOT NULL,             -- when the Discord message was posted
    clip_url           TEXT NOT NULL DEFAULT '',  -- Twitch clip URL, else first link, else attachment URL
    twitch_clip_id     TEXT NOT NULL DEFAULT '',  -- slug, when clip_url is a Twitch clip
    title              TEXT NOT NULL DEFAULT '',  -- Twitch clip title, else the message text without links
    clipper_name       TEXT NOT NULL DEFAULT '',  -- Twitch account that made the clip
    broadcaster_name   TEXT NOT NULL DEFAULT '',
    thumbnail_url      TEXT NOT NULL DEFAULT '',  -- from the Twitch API only
    duration_seconds   INTEGER NOT NULL DEFAULT 0,
    clip_created_at    TEXT,
    twitch_lookup_at   TEXT,                      -- NULL = not looked up yet
    status             TEXT NOT NULL DEFAULT 'new'
                       CHECK (status IN ('new', 'discarded', 'pending', 'edited', 'posted')),
    status_at          TEXT NOT NULL,             -- when status last changed
    reviewed_by        TEXT NOT NULL DEFAULT '',  -- who first moved it out of the inbox
    reviewed_at        TEXT,
    edited_title       TEXT NOT NULL DEFAULT '',
    edited_url         TEXT NOT NULL DEFAULT '',
    youtube_url        TEXT NOT NULL DEFAULT '',
    instagram_url      TEXT NOT NULL DEFAULT '',
    x_url              TEXT NOT NULL DEFAULT '',
    tiktok_url         TEXT NOT NULL DEFAULT '',
    notes              TEXT NOT NULL DEFAULT '',
    first_posted_at    TEXT,                      -- first time it entered 'posted'
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
CREATE INDEX clips_server_status ON clips(server_id, status, status_at);
CREATE INDEX clips_twitch_lookup ON clips(server_id, twitch_lookup_at);
