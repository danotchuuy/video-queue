-- YouTube details for the first YouTube link in a suggestion, filled in after sync.
ALTER TABLE suggestions ADD COLUMN youtube_video_id TEXT NOT NULL DEFAULT '';
ALTER TABLE suggestions ADD COLUMN video_title      TEXT NOT NULL DEFAULT '';
ALTER TABLE suggestions ADD COLUMN channel_title    TEXT NOT NULL DEFAULT '';
ALTER TABLE suggestions ADD COLUMN duration_seconds INTEGER NOT NULL DEFAULT 0;
-- NULL = not looked up yet (also reset when the message content changes).
ALTER TABLE suggestions ADD COLUMN video_lookup_at  TEXT;

CREATE INDEX suggestions_video_lookup ON suggestions(server_id, video_lookup_at);
