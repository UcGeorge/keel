-- Run outputs: environment variables captured from the container at the
-- end of a fully successful run. Values are encrypted at rest like target
-- values; is_secret drives masking in the UI.
CREATE TABLE run_outputs (
    run_id     uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    name       text NOT NULL,
    value_enc  bytea NOT NULL,
    is_secret  boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, name)
);
