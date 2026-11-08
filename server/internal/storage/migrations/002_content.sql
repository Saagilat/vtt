CREATE TABLE images (
    id          TEXT PRIMARY KEY,
    campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    sha256      TEXT NOT NULL,
    width       INTEGER NOT NULL,
    height      INTEGER NOT NULL,
    mime        TEXT NOT NULL,
    data        BLOB NOT NULL,
    UNIQUE (campaign_id, sha256)
);

CREATE TABLE scenes (
    id          TEXT PRIMARY KEY,
    campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    image_id    TEXT NOT NULL REFERENCES images(id),
    name        TEXT NOT NULL,
    annotation  TEXT NOT NULL,
    light       TEXT NOT NULL DEFAULT 'bright',
    active      INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL
);

CREATE INDEX scenes_campaign ON scenes(campaign_id);
