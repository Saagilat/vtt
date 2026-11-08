ALTER TABLE campaigns ADD COLUMN archived_at TEXT;

CREATE INDEX campaigns_archived ON campaigns(archived_at);
