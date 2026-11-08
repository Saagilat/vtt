CREATE TABLE characters (
    id          TEXT PRIMARY KEY,
    campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    owner_id    TEXT REFERENCES accounts(id) ON DELETE SET NULL,
    name        TEXT NOT NULL,
    data        TEXT NOT NULL,
    approved    INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL
);

CREATE INDEX characters_campaign ON characters(campaign_id);
