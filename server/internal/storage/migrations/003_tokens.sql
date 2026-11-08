CREATE TABLE tokens (
    id          TEXT PRIMARY KEY,
    campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    scene_id    TEXT NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,
    owner_id    TEXT REFERENCES accounts(id),
    name        TEXT NOT NULL,
    x           REAL NOT NULL,
    y           REAL NOT NULL,
    color       TEXT NOT NULL DEFAULT '#e8c547',
    created_at  TEXT NOT NULL
);

CREATE INDEX tokens_scene ON tokens(scene_id);
