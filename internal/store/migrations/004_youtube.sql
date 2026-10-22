-- YouTube metadata. IDs are derived server-side from the URL; thumbnails are
-- built from the video ID (https://i.ytimg.com/vi/{id}/...), never stored as URLs.

ALTER TABLE videos ADD COLUMN youtube_video_id TEXT NOT NULL DEFAULT '';
ALTER TABLE videos ADD COLUMN channel_title    TEXT NOT NULL DEFAULT '';
ALTER TABLE videos ADD COLUMN duration_seconds INTEGER NOT NULL DEFAULT 0;

ALTER TABLE series ADD COLUMN youtube_playlist_id TEXT NOT NULL DEFAULT '';
ALTER TABLE series ADD COLUMN thumbnail_video_id  TEXT NOT NULL DEFAULT '';

CREATE INDEX videos_series_youtube ON videos(series_id, youtube_video_id);
