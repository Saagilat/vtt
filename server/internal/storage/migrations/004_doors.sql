CREATE TABLE door_states (
    scene_id TEXT NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,
    door_id  TEXT NOT NULL,
    open     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (scene_id, door_id)
);
