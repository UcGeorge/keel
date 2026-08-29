-- Email notifications: per-organization SMTP settings, recipients with the
-- events they subscribe to, and a short delivery log for troubleshooting.
CREATE TABLE org_smtp_settings (
    org_id          uuid PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    host            text NOT NULL,
    port            integer NOT NULL DEFAULT 587,
    username        text NOT NULL DEFAULT '',
    password_enc    bytea,
    encryption      text NOT NULL DEFAULT 'starttls' CHECK (encryption IN ('starttls', 'tls', 'none')),
    from_address    text NOT NULL,
    from_name       text NOT NULL DEFAULT '',
    last_test_at    timestamptz,
    last_test_error text NOT NULL DEFAULT '',
    updated_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_recipients (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    email      text NOT NULL,
    events     text[] NOT NULL DEFAULT '{}',
    enabled    boolean NOT NULL DEFAULT true,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX notification_recipients_org_email_idx ON notification_recipients (org_id, lower(email));

CREATE TABLE notification_deliveries (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    event      text NOT NULL,
    subject    text NOT NULL,
    recipients text[] NOT NULL DEFAULT '{}',
    status     text NOT NULL CHECK (status IN ('sent', 'failed')),
    error      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_deliveries_org_idx ON notification_deliveries (org_id, created_at DESC);
