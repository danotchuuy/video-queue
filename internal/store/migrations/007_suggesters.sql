-- Who suggested what, and self-suggestion detection.

ALTER TABLE suggestions ADD COLUMN author_username TEXT NOT NULL DEFAULT ''; -- Discord username (author_name is the display name)
ALTER TABLE suggestions ADD COLUMN channel_handle  TEXT NOT NULL DEFAULT ''; -- YouTube @handle of the linked video's channel
ALTER TABLE suggestions ADD COLUMN self_suggestion INTEGER NOT NULL DEFAULT 0; -- suggester's name matches the video's channel
ALTER TABLE suggestions ADD COLUMN discard_reason  TEXT NOT NULL DEFAULT ''; -- '' (manual) or 'self_suggestion' (automatic)
ALTER TABLE suggestions ADD COLUMN self_override   INTEGER NOT NULL DEFAULT 0; -- a mod restored an auto-discard; don't redo it
CREATE INDEX suggestions_author ON suggestions(server_id, author_id);

ALTER TABLE videos ADD COLUMN suggested_by_id   TEXT NOT NULL DEFAULT ''; -- Discord user ID of the suggester
ALTER TABLE videos ADD COLUMN suggested_by_name TEXT NOT NULL DEFAULT '';

ALTER TABLE servers ADD COLUMN block_self_suggestions INTEGER NOT NULL DEFAULT 0;

-- Re-run lookups so existing suggestions get channel handles and self-suggestion flags.
UPDATE suggestions SET video_lookup_at = NULL;
