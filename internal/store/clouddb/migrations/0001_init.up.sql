-- Keel Cloud schema. Requires PostgreSQL 13+ (gen_random_uuid).
-- Emails and slugs are normalized to lowercase by the application; the
-- expression indexes below enforce case-insensitive uniqueness.

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL,
    name          text NOT NULL,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_idx ON users (lower(email));

CREATE TABLE sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    csrf_token text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE orgs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text NOT NULL,
    name       text NOT NULL,
    personal   boolean NOT NULL DEFAULT false,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX orgs_slug_idx ON orgs (lower(slug));

CREATE TABLE org_members (
    org_id        uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role          text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    can_configure boolean NOT NULL DEFAULT false,
    can_deploy    boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX org_members_user_idx ON org_members (user_id);

CREATE TABLE org_invites (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    email         text NOT NULL,
    role          text NOT NULL CHECK (role IN ('admin', 'member')),
    can_configure boolean NOT NULL DEFAULT false,
    can_deploy    boolean NOT NULL DEFAULT false,
    token_hash    bytea NOT NULL UNIQUE,
    invited_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    accepted_at   timestamptz
);
CREATE INDEX org_invites_org_idx ON org_invites (org_id);

CREATE TABLE github_installations (
    installation_id bigint PRIMARY KEY,
    account_login   text NOT NULL,
    account_type    text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE repos (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                 uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name                   text NOT NULL,
    provider               text NOT NULL CHECK (provider IN ('git', 'github_app')),
    git_url                text NOT NULL DEFAULT '',
    branch                 text NOT NULL DEFAULT 'main',
    auth_token_enc         bytea,
    github_installation_id bigint,
    github_full_name       text NOT NULL DEFAULT '',
    status                 text NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending', 'ok', 'config_missing', 'config_invalid', 'error')),
    config_yaml            text NOT NULL DEFAULT '',
    config_error           text NOT NULL DEFAULT '',
    last_commit_sha        text NOT NULL DEFAULT '',
    last_synced_at         timestamptz,
    created_by             uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX repos_org_name_idx ON repos (org_id, lower(name));
CREATE INDEX repos_github_idx ON repos (github_installation_id, lower(github_full_name));

CREATE TABLE targets (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id     uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    deployment  text NOT NULL,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    auto_deploy boolean NOT NULL DEFAULT false,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX targets_repo_dep_name_idx ON targets (repo_id, deployment, lower(name));

CREATE TABLE target_values (
    target_id  uuid NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
    var_name   text NOT NULL,
    value_enc  bytea NOT NULL,
    is_secret  boolean NOT NULL DEFAULT false,
    updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (target_id, var_name)
);

CREATE TABLE runs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id      uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    target_id    uuid REFERENCES targets(id) ON DELETE SET NULL,
    deployment   text NOT NULL,
    target_name  text NOT NULL,
    status       text NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'building', 'running', 'succeeded', 'failed', 'canceled')),
    trigger_kind text NOT NULL DEFAULT 'manual' CHECK (trigger_kind IN ('manual', 'push')),
    commit_sha   text NOT NULL DEFAULT '',
    exit_code    integer,
    failed_step  integer,
    error        text NOT NULL DEFAULT '',
    started_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz
);
CREATE INDEX runs_repo_idx ON runs (repo_id, created_at DESC);
CREATE INDEX runs_target_idx ON runs (target_id, created_at DESC);

CREATE TABLE run_steps (
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    idx    integer NOT NULL,
    name   text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    PRIMARY KEY (run_id, idx)
);

CREATE TABLE run_logs (
    run_id     uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq        integer NOT NULL,
    line       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, seq)
);
