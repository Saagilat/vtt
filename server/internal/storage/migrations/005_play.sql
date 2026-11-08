CREATE TABLE invites (
    code        TEXT PRIMARY KEY,
    campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    created_by  TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    accepted_by TEXT REFERENCES accounts(id) ON DELETE SET NULL,
    accepted_at TEXT
);

CREATE INDEX invites_campaign ON invites(campaign_id);

ALTER TABLE tokens ADD COLUMN hp_current INTEGER NOT NULL DEFAULT 10;
ALTER TABLE tokens ADD COLUMN hp_max INTEGER NOT NULL DEFAULT 10;
ALTER TABLE tokens ADD COLUMN initiative INTEGER NOT NULL DEFAULT 0;

CREATE TABLE table_turns (
    campaign_id TEXT PRIMARY KEY REFERENCES campaigns(id) ON DELETE CASCADE,
    token_id    TEXT NOT NULL DEFAULT '',
    round       INTEGER NOT NULL DEFAULT 1
);
