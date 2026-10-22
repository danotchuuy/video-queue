-- Link to the edited video file (e.g. a Drive/Dropbox share) for the editing stage.
ALTER TABLE videos ADD COLUMN edit_url TEXT NOT NULL DEFAULT '';
