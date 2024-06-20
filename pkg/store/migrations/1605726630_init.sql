CREATE TABLE IF NOT EXISTS "dialog"
(
    "id"              TEXT PRIMARY KEY,
    "publication"     TEXT      NOT NULL,
    "series"          INTEGER   NOT NULL,
    "episode"         INTEGER   NOT NULL,
    "pos"             INTEGER   NOT NULL,
    "start_timestamp" TIMESTAMP NULL,
    "end_timestamp"   INTEGER   NULL,
    "content"         TEXT      NOT NULL
);

CREATE INDEX dialog_pos ON dialog ("pos");
CREATE INDEX ts ON dialog ("start_timestamp");
