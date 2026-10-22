-- Editor roles: clip editors see only the clip pages, video editors only the
-- video pages (queue, pipeline, series). Mods/managers/admins see everything.
ALTER TABLE servers ADD COLUMN clip_editor_role_ids  TEXT NOT NULL DEFAULT '[]'; -- JSON array of role IDs
ALTER TABLE servers ADD COLUMN video_editor_role_ids TEXT NOT NULL DEFAULT '[]';

-- Access is now per section.
ALTER TABLE server_access ADD COLUMN can_videos INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_access ADD COLUMN can_clips  INTEGER NOT NULL DEFAULT 0;
DELETE FROM server_access; -- re-check everyone with the new rules
