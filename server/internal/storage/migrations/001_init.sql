CREATE TABLE accounts (
    id            TEXT PRIMARY KEY,
    login         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL
);

CREATE TABLE campaigns (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE memberships (
    account_id  TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    role        TEXT NOT NULL,
    PRIMARY KEY (account_id, campaign_id)
);

CREATE TABLE sessions (
    token      TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL
);

CREATE INDEX sessions_account ON sessions(account_id);
