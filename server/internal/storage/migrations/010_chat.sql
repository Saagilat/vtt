CREATE TABLE chat_messages (
    id          TEXT PRIMARY KEY,
    campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    account_id  TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    text        TEXT NOT NULL,
    channel     TEXT NOT NULL DEFAULT 'table',
    created_at  TEXT NOT NULL
);

CREATE INDEX chat_messages_campaign ON chat_messages(campaign_id, created_at);
