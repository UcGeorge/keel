-- Run outputs: environment variables captured from the container at the
-- end of a fully successful run. Values are encrypted at rest like target
-- values; is_secret drives masking in the UI.
CREATE TABLE run_outputs (
    run_id     TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    value_enc  BLOB NOT NULL,
    is_secret  INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (run_id, name)
);
