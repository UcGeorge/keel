-- Users ----------------------------------------------------------------------

-- name: CreateUser :one
INSERT INTO users (email, name, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower($1);

-- name: UpdateUserProfile :one
UPDATE users SET name = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- Sessions -------------------------------------------------------------------

-- name: CreateSession :one
INSERT INTO sessions (user_id, token_hash, csrf_token, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetSessionByTokenHash :one
SELECT * FROM sessions WHERE token_hash = $1 AND expires_at > now();

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= now();

-- Orgs -----------------------------------------------------------------------

-- name: CreateOrg :one
INSERT INTO orgs (slug, name, personal, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetOrg :one
SELECT * FROM orgs WHERE id = $1;

-- name: GetOrgBySlug :one
SELECT * FROM orgs WHERE lower(slug) = lower($1);

-- name: UpdateOrgName :one
UPDATE orgs SET name = $2 WHERE id = $1
RETURNING *;

-- name: DeleteOrg :exec
DELETE FROM orgs WHERE id = $1;

-- name: ListOrgsForUser :many
SELECT o.*, m.role
FROM orgs o
JOIN org_members m ON m.org_id = o.id
WHERE m.user_id = $1
ORDER BY o.personal DESC, o.name;

-- Org members ----------------------------------------------------------------

-- name: CreateOrgMember :one
INSERT INTO org_members (org_id, user_id, role, can_configure, can_deploy)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetOrgMember :one
SELECT * FROM org_members WHERE org_id = $1 AND user_id = $2;

-- name: ListOrgMembers :many
SELECT m.*, u.email, u.name
FROM org_members m
JOIN users u ON u.id = m.user_id
WHERE m.org_id = $1
ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, u.name;

-- name: UpdateOrgMember :one
UPDATE org_members SET role = $3, can_configure = $4, can_deploy = $5
WHERE org_id = $1 AND user_id = $2
RETURNING *;

-- name: DeleteOrgMember :exec
DELETE FROM org_members WHERE org_id = $1 AND user_id = $2;

-- name: CountOrgOwners :one
SELECT count(*) FROM org_members WHERE org_id = $1 AND role = 'owner';

-- Org invites ----------------------------------------------------------------

-- name: CreateInvite :one
INSERT INTO org_invites (org_id, email, role, can_configure, can_deploy, token_hash, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetInviteByTokenHash :one
SELECT * FROM org_invites
WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > now();

-- name: ListOrgInvites :many
SELECT * FROM org_invites
WHERE org_id = $1 AND accepted_at IS NULL AND expires_at > now()
ORDER BY created_at DESC;

-- name: AcceptInvite :exec
UPDATE org_invites SET accepted_at = now() WHERE id = $1;

-- name: DeleteInvite :exec
DELETE FROM org_invites WHERE id = $1 AND org_id = $2;

-- GitHub installations -------------------------------------------------------

-- name: UpsertGithubInstallation :exec
INSERT INTO github_installations (installation_id, account_login, account_type)
VALUES ($1, $2, $3)
ON CONFLICT (installation_id)
DO UPDATE SET account_login = excluded.account_login, account_type = excluded.account_type;

-- name: DeleteGithubInstallation :exec
DELETE FROM github_installations WHERE installation_id = $1;

-- name: ListGithubInstallations :many
SELECT * FROM github_installations ORDER BY account_login;

-- Repos ----------------------------------------------------------------------

-- name: CreateRepo :one
INSERT INTO repos (org_id, name, provider, git_url, branch, auth_token_enc, github_installation_id, github_full_name, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetRepo :one
SELECT * FROM repos WHERE id = $1;

-- name: GetRepoInOrg :one
SELECT * FROM repos WHERE org_id = $1 AND id = $2;

-- name: GetRepoByName :one
SELECT * FROM repos WHERE org_id = $1 AND lower(name) = lower($2);

-- name: ListReposForOrg :many
SELECT * FROM repos WHERE org_id = $1 ORDER BY name;

-- name: ListReposByGithubRepo :many
SELECT * FROM repos
WHERE github_installation_id = $1 AND lower(github_full_name) = lower($2);

-- name: UpdateRepoSettings :one
UPDATE repos
SET name = $2, branch = $3, git_url = $4, auth_token_enc = $5, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateRepoSync :exec
UPDATE repos
SET status = $2, config_yaml = $3, config_error = $4, last_commit_sha = $5,
    last_synced_at = now(), updated_at = now()
WHERE id = $1;

-- name: DeleteRepo :exec
DELETE FROM repos WHERE id = $1;

-- Targets --------------------------------------------------------------------

-- name: CreateTarget :one
INSERT INTO targets (repo_id, deployment, name, description, auto_deploy, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetTarget :one
SELECT * FROM targets WHERE id = $1;

-- name: GetTargetByName :one
SELECT * FROM targets
WHERE repo_id = $1 AND deployment = $2 AND lower(name) = lower($3);

-- name: ListTargetsForRepo :many
SELECT * FROM targets WHERE repo_id = $1 ORDER BY deployment, name;

-- name: ListTargetsForDeployment :many
SELECT * FROM targets WHERE repo_id = $1 AND deployment = $2 ORDER BY name;

-- name: ListAutoDeployTargets :many
SELECT * FROM targets WHERE repo_id = $1 AND auto_deploy = true ORDER BY deployment, name;

-- name: UpdateTarget :one
UPDATE targets SET name = $2, description = $3, auto_deploy = $4, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteTarget :exec
DELETE FROM targets WHERE id = $1;

-- Target values --------------------------------------------------------------

-- name: UpsertTargetValue :exec
INSERT INTO target_values (target_id, var_name, value_enc, is_secret, updated_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (target_id, var_name)
DO UPDATE SET value_enc = excluded.value_enc,
              is_secret = excluded.is_secret,
              updated_by = excluded.updated_by,
              updated_at = now();

-- name: ListTargetValues :many
SELECT * FROM target_values WHERE target_id = $1 ORDER BY var_name;

-- name: DeleteTargetValue :exec
DELETE FROM target_values WHERE target_id = $1 AND var_name = $2;

-- Runs -----------------------------------------------------------------------

-- name: CreateRun :one
INSERT INTO runs (repo_id, target_id, deployment, target_name, trigger_kind, commit_sha, started_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE id = $1;

-- name: ListRunsForRepo :many
SELECT * FROM runs WHERE repo_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: ListRunsForTarget :many
SELECT * FROM runs WHERE target_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: ListRunsForOrg :many
SELECT r.*, rp.name AS repo_name
FROM runs r
JOIN repos rp ON rp.id = r.repo_id
WHERE rp.org_id = $1
ORDER BY r.created_at DESC, r.id DESC
LIMIT $2;

-- name: CountActiveRunsForTarget :one
SELECT count(*) FROM runs
WHERE target_id = $1 AND status IN ('queued', 'building', 'running');

-- name: SetRunCommit :exec
UPDATE runs SET commit_sha = $2 WHERE id = $1;

-- name: StartRun :exec
UPDATE runs SET status = $2, started_at = now() WHERE id = $1;

-- name: SetRunStatus :exec
UPDATE runs SET status = $2 WHERE id = $1;

-- name: FinishRun :exec
UPDATE runs SET status = $2, exit_code = $3, failed_step = $4, error = $5, finished_at = now()
WHERE id = $1;

-- name: MarkInterruptedRuns :exec
UPDATE runs
SET status = 'failed', error = 'interrupted: the server was stopped while the run was in progress', finished_at = now()
WHERE status IN ('queued', 'building', 'running');

-- Run steps ------------------------------------------------------------------

-- name: InsertRunStep :exec
INSERT INTO run_steps (run_id, idx, name, status) VALUES ($1, $2, $3, 'pending');

-- name: SetRunStepStatus :exec
UPDATE run_steps SET status = $3 WHERE run_id = $1 AND idx = $2;

-- name: SkipUnfinishedRunSteps :exec
UPDATE run_steps SET status = 'skipped'
WHERE run_id = $1 AND status IN ('pending', 'running');

-- name: ListRunSteps :many
SELECT * FROM run_steps WHERE run_id = $1 ORDER BY idx;

-- Run logs -------------------------------------------------------------------

-- name: AppendRunLog :exec
INSERT INTO run_logs (run_id, seq, line) VALUES ($1, $2, $3);

-- name: ListRunLogs :many
SELECT * FROM run_logs WHERE run_id = $1 ORDER BY seq;

-- name: ListRunLogsAfter :many
SELECT * FROM run_logs WHERE run_id = $1 AND seq > $2 ORDER BY seq;

-- name: InsertRunOutput :exec
INSERT INTO run_outputs (run_id, name, value_enc, is_secret)
VALUES ($1, $2, $3, $4)
ON CONFLICT (run_id, name) DO UPDATE SET value_enc = EXCLUDED.value_enc, is_secret = EXCLUDED.is_secret;

-- name: ListRunOutputs :many
SELECT * FROM run_outputs WHERE run_id = $1 ORDER BY name;

-- name: LatestSucceededRunForTarget :one
SELECT * FROM runs WHERE target_id = $1 AND status = 'succeeded'
ORDER BY created_at DESC, id DESC LIMIT 1;

-- Run inputs -----------------------------------------------------------------

-- name: InsertRunInput :exec
INSERT INTO run_inputs (run_id, idx, name, label, value_enc, is_secret, deploy_time, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListRunInputs :many
SELECT * FROM run_inputs WHERE run_id = $1 ORDER BY idx;

-- name: ListRunDeployInputsForRuns :many
SELECT * FROM run_inputs
WHERE run_id = ANY($1::uuid[]) AND deploy_time = true AND is_secret = false
ORDER BY run_id, idx;

-- SMTP settings --------------------------------------------------------------

-- name: GetOrgSMTP :one
SELECT * FROM org_smtp_settings WHERE org_id = $1;

-- name: UpsertOrgSMTP :exec
INSERT INTO org_smtp_settings (org_id, host, port, username, password_enc, encryption, from_address, from_name, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (org_id) DO UPDATE SET
    host = excluded.host, port = excluded.port, username = excluded.username,
    password_enc = excluded.password_enc, encryption = excluded.encryption,
    from_address = excluded.from_address, from_name = excluded.from_name,
    updated_by = excluded.updated_by, updated_at = now();

-- name: SetOrgSMTPTest :exec
UPDATE org_smtp_settings SET last_test_at = now(), last_test_error = $2 WHERE org_id = $1;

-- name: DeleteOrgSMTP :exec
DELETE FROM org_smtp_settings WHERE org_id = $1;

-- Notification recipients ----------------------------------------------------

-- name: CreateRecipient :one
INSERT INTO notification_recipients (org_id, email, events, enabled, include_insight, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetRecipient :one
SELECT * FROM notification_recipients WHERE id = $1 AND org_id = $2;

-- name: ListRecipients :many
SELECT * FROM notification_recipients WHERE org_id = $1 ORDER BY lower(email);

-- name: ListRecipientsForEvent :many
SELECT * FROM notification_recipients
WHERE org_id = $1 AND enabled = true AND $2::text = ANY(events)
ORDER BY lower(email);

-- name: UpdateRecipient :exec
UPDATE notification_recipients SET email = $3, events = $4, enabled = $5, include_insight = $6, updated_at = now()
WHERE id = $1 AND org_id = $2;

-- name: DeleteRecipient :exec
DELETE FROM notification_recipients WHERE id = $1 AND org_id = $2;

-- Notification deliveries ----------------------------------------------------

-- name: InsertDelivery :exec
INSERT INTO notification_deliveries (org_id, event, subject, recipients, status, error)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListDeliveries :many
SELECT * FROM notification_deliveries WHERE org_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: PruneDeliveries :exec
DELETE FROM notification_deliveries d
WHERE d.org_id = $1 AND d.id NOT IN (
    SELECT k.id FROM notification_deliveries k WHERE k.org_id = $1 ORDER BY k.created_at DESC, k.id DESC LIMIT $2
);

-- AI settings and insights ---------------------------------------------------

-- name: GetOrgAI :one
SELECT * FROM org_ai_settings WHERE org_id = $1;

-- name: UpsertOrgAI :exec
INSERT INTO org_ai_settings (org_id, base_url, api_key_enc, model, verified_at, updated_by)
VALUES ($1, $2, $3, $4, now(), $5)
ON CONFLICT (org_id) DO UPDATE SET
    base_url = excluded.base_url, api_key_enc = excluded.api_key_enc, model = excluded.model,
    verified_at = now(), updated_by = excluded.updated_by, updated_at = now();

-- name: DeleteOrgAI :exec
DELETE FROM org_ai_settings WHERE org_id = $1;

-- name: UpsertRunInsight :exec
INSERT INTO run_insights (run_id, model, content, created_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (run_id) DO UPDATE SET
    model = excluded.model, content = excluded.content, created_by = excluded.created_by, created_at = now();

-- name: GetRunInsight :one
SELECT * FROM run_insights WHERE run_id = $1;
