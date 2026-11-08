ALTER TABLE tokens ADD COLUMN character_id TEXT REFERENCES characters(id) ON DELETE SET NULL;

CREATE INDEX tokens_character ON tokens(character_id);
