-- OAuth credentials are opaque, audience-bound and hashed at rest. Grants
-- are shared by every replica; revocation is checked on every MCP request.
CREATE TABLE mcp_clients (
    id text PRIMARY KEY,
    name text NOT NULL,
    redirect_uris text[] NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now() + interval '90 days'
);
CREATE TABLE mcp_grants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id text NOT NULL REFERENCES mcp_clients(id) ON DELETE CASCADE,
    resource text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('keel:read', 'keel:full')),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now() + interval '30 days',
    revoked_at timestamptz
);
CREATE INDEX mcp_grants_user_idx ON mcp_grants(user_id);
CREATE TABLE mcp_codes (
    hash bytea PRIMARY KEY,
    grant_id uuid NOT NULL REFERENCES mcp_grants(id) ON DELETE CASCADE,
    redirect_uri text NOT NULL,
    challenge text NOT NULL,
    expires_at timestamptz NOT NULL DEFAULT now() + interval '5 minutes',
    used_at timestamptz
);
CREATE TABLE mcp_tokens (
    hash bytea PRIMARY KEY,
    grant_id uuid NOT NULL REFERENCES mcp_grants(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('access', 'refresh')),
    expires_at timestamptz NOT NULL,
    used_at timestamptz
);
CREATE INDEX mcp_tokens_grant_idx ON mcp_tokens(grant_id);
-- Database-backed quotas apply across replicas and expire automatically.
CREATE TABLE mcp_rate_limits (
    bucket text PRIMARY KEY,
    starts_at timestamptz NOT NULL DEFAULT now(),
    hits integer NOT NULL
);
CREATE TABLE mcp_audit (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    -- Retain the historical connection ID after expired credentials are pruned.
    grant_id uuid NOT NULL,
    tool text NOT NULL,
    status integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mcp_audit_created_idx ON mcp_audit(created_at);
