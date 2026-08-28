-- Keel Dev local state. Timestamps are unix milliseconds (UTC).

CREATE TABLE targets (
    id          TEXT PRIMARY KEY,
    deployment  TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (deployment, name)
);

CREATE TABLE target_values (
    target_id  TEXT NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
    var_name   TEXT NOT NULL,
    value_enc  BLOB NOT NULL,
    is_secret  INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (target_id, var_name)
);

CREATE TABLE runs (
    id          TEXT PRIMARY KEY,
    target_id   TEXT REFERENCES targets(id) ON DELETE SET NULL,
    deployment  TEXT NOT NULL,
    target_name TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'queued',
    exit_code   INTEGER,
    failed_step INTEGER,
    error       TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER
);

CREATE INDEX runs_target_idx ON runs(target_id, created_at DESC);
CREATE INDEX runs_created_idx ON runs(created_at DESC);

CREATE TABLE run_steps (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    idx    INTEGER NOT NULL,
    name   TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    PRIMARY KEY (run_id, idx)
);

CREATE TABLE run_logs (
    run_id     TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    line       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (run_id, seq)
);
