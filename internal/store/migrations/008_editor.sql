-- Who is editing the video. editor_id is a Discord user ID when the editor uses
-- the app; for editors who don't, it's '' and only editor_name is set.
ALTER TABLE videos ADD COLUMN editor_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE videos ADD COLUMN editor_name TEXT NOT NULL DEFAULT '';
