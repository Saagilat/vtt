CREATE TABLE explored_masks (
    campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    scene_id    TEXT NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,
    account_id  TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    data        BLOB NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (campaign_id, scene_id, account_id)
);
