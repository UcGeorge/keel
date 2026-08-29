-- Run inputs: the variable values a run was started with, snapshotted at
-- creation so a run stays explainable after the target's values or the
-- configuration change. Secrets record that a value was set, never the
-- value; non-secret values are encrypted at rest like target values.
CREATE TABLE run_inputs (
    run_id      uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    idx         integer NOT NULL,
    name        text NOT NULL,
    label       text NOT NULL DEFAULT '',
    value_enc   bytea,
    is_secret   boolean NOT NULL DEFAULT false,
    deploy_time boolean NOT NULL DEFAULT false,
    source      text NOT NULL CHECK (source IN ('saved', 'deploy', 'default', 'unset', 'inactive')),
    PRIMARY KEY (run_id, name)
);
