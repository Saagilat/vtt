CREATE TABLE scene_turns (
    campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    scene_id    TEXT NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,
    token_id    TEXT NOT NULL DEFAULT '',
    round       INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (scene_id)
);
