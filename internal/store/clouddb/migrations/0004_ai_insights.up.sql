-- AI insights: per-organization OpenAI-compatible model settings and the
-- generated explanation of a failed run.
CREATE TABLE org_ai_settings (
    org_id      uuid PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    base_url    text NOT NULL,
    api_key_enc bytea,
    model       text NOT NULL,
    verified_at timestamptz,
    updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE run_insights (
    run_id     uuid PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    model      text NOT NULL,
    content    text NOT NULL,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
