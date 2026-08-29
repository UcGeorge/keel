-- Targets -------------------------------------------------------------------

-- name: CreateTarget :one
INSERT INTO targets (id, deployment, name, description, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetTarget :one
SELECT * FROM targets WHERE id = ?;

-- name: GetTargetByName :one
SELECT * FROM targets WHERE deployment = ? AND name = ?;

-- name: ListTargets :many
SELECT * FROM targets ORDER BY deployment, name;

-- name: ListTargetsByDeployment :many
SELECT * FROM targets WHERE deployment = ? ORDER BY name;

-- name: UpdateTarget :one
UPDATE targets SET name = ?, description = ?, updated_at = ?
WHERE id = ?
RETURNING *;

-- name: DeleteTarget :exec
DELETE FROM targets WHERE id = ?;

-- Target values --------------------------------------------------------------

-- name: UpsertTargetValue :exec
INSERT INTO target_values (target_id, var_name, value_enc, is_secret, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (target_id, var_name)
DO UPDATE SET value_enc = excluded.value_enc,
              is_secret = excluded.is_secret,
              updated_at = excluded.updated_at;

-- name: ListTargetValues :many
SELECT * FROM target_values WHERE target_id = ? ORDER BY var_name;

-- name: DeleteTargetValue :exec
DELETE FROM target_values WHERE target_id = ? AND var_name = ?;

-- Runs -----------------------------------------------------------------------

-- name: CreateRun :one
INSERT INTO runs (id, target_id, deployment, target_name, status, created_at)
VALUES (?, ?, ?, ?, 'queued', ?)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE id = ?;

-- name: ListRuns :many
SELECT * FROM runs ORDER BY created_at DESC, id DESC LIMIT ?;

-- name: ListRunsByTarget :many
SELECT * FROM runs WHERE target_id = ? ORDER BY created_at DESC, id DESC LIMIT ?;

-- name: ListActiveRuns :many
SELECT * FROM runs WHERE status IN ('queued', 'building', 'running')
ORDER BY created_at DESC;

-- name: StartRun :exec
UPDATE runs SET status = ?, started_at = ? WHERE id = ?;

-- name: SetRunStatus :exec
UPDATE runs SET status = ? WHERE id = ?;

-- name: FinishRun :exec
UPDATE runs SET status = ?, exit_code = ?, failed_step = ?, error = ?, finished_at = ?
WHERE id = ?;

-- name: MarkInterruptedRuns :exec
UPDATE runs SET status = 'failed', error = 'interrupted: keel dev was stopped while the run was in progress', finished_at = ?
WHERE status IN ('queued', 'building', 'running');

-- Run steps ------------------------------------------------------------------

-- name: InsertRunStep :exec
INSERT INTO run_steps (run_id, idx, name, status) VALUES (?, ?, ?, 'pending');

-- name: SetRunStepStatus :exec
UPDATE run_steps SET status = ? WHERE run_id = ? AND idx = ?;

-- name: SkipUnfinishedRunSteps :exec
UPDATE run_steps SET status = 'skipped'
WHERE run_id = ? AND status IN ('pending', 'running');

-- name: ListRunSteps :many
SELECT * FROM run_steps WHERE run_id = ? ORDER BY idx;

-- Run logs -------------------------------------------------------------------

-- name: AppendRunLog :exec
INSERT INTO run_logs (run_id, seq, line, created_at) VALUES (?, ?, ?, ?);

-- name: ListRunLogs :many
SELECT * FROM run_logs WHERE run_id = ? ORDER BY seq;

-- name: ListRunLogsAfter :many
SELECT * FROM run_logs WHERE run_id = ? AND seq > ? ORDER BY seq;

-- name: InsertRunOutput :exec
INSERT INTO run_outputs (run_id, name, value_enc, is_secret, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (run_id, name) DO UPDATE SET value_enc = excluded.value_enc, is_secret = excluded.is_secret;

-- name: ListRunOutputs :many
SELECT * FROM run_outputs WHERE run_id = ? ORDER BY name;

-- name: LatestSucceededRunForTarget :one
SELECT * FROM runs WHERE target_id = ? AND status = 'succeeded'
ORDER BY created_at DESC, id DESC LIMIT 1;

-- Run inputs -----------------------------------------------------------------

-- name: InsertRunInput :exec
INSERT INTO run_inputs (run_id, idx, name, label, value_enc, is_secret, deploy_time, source)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListRunInputs :many
SELECT * FROM run_inputs WHERE run_id = ? ORDER BY idx;

-- name: ListRunDeployInputsForRuns :many
SELECT * FROM run_inputs
WHERE run_id IN (sqlc.slice('run_ids')) AND deploy_time = 1 AND is_secret = 0
ORDER BY run_id, idx;
