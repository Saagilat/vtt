ALTER TABLE notes ADD COLUMN visibility TEXT NOT NULL DEFAULT 'shared';

CREATE INDEX notes_visibility ON notes(campaign_id, visibility);
