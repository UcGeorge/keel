-- Keep historical identities and resource labels even after the source rows
-- are renamed or deleted. In particular, deleting an org must not delete the
-- evidence of its deletion. The application always reads by an authorized org ID.
CREATE TABLE org_audit_events (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id        uuid NOT NULL,
    occurred_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    actor_id      text NOT NULL DEFAULT '',
    actor_name    text NOT NULL,
    actor_email   text NOT NULL DEFAULT '',
    source        text NOT NULL CHECK (source IN ('web', 'mcp', 'github')),
    action        text NOT NULL,
    category      text NOT NULL,
    outcome       text NOT NULL CHECK (outcome IN ('succeeded', 'failed', 'denied', 'partial', 'no_change')),
    resource_type text NOT NULL,
    resource_id   uuid,
    resource_name text NOT NULL,
    detail        text NOT NULL DEFAULT ''
);

CREATE INDEX org_audit_events_org_id_idx ON org_audit_events (org_id, id DESC);
