-- Run inputs: the variable values a run was started with, snapshotted at
-- creation. Secrets record that a value was set, never the value;
-- non-secret values are encrypted at rest like target values.
CREATE TABLE run_inputs (
    run_id      TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    idx         INTEGER NOT NULL,
    name        TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    value_enc   BLOB,
    is_secret   INTEGER NOT NULL DEFAULT 0,
    deploy_time INTEGER NOT NULL DEFAULT 0,
    source      TEXT NOT NULL,
    PRIMARY KEY (run_id, name)
);
