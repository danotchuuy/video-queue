-- Removed suggestions are hidden, not deleted: the poller re-reads recent Discord
-- messages, so a deleted row would be re-imported on the next sync.
ALTER TABLE suggestions ADD COLUMN removed_at TEXT;
CREATE INDEX suggestions_removed ON suggestions(server_id, removed_at);
