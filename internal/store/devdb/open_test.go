package devdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func sqlNullString(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func TestOpenMigrateAndQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Opening again must be a no-op (migrations already applied).
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	db2.Close()

	q := New(db)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	tgt, err := q.CreateTarget(ctx, CreateTargetParams{
		ID: "t1", Deployment: "prod", Name: "client-a", Description: "d",
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertTargetValue(ctx, UpsertTargetValueParams{
		TargetID: tgt.ID, VarName: "API_KEY", ValueEnc: []byte{1, 2, 3}, IsSecret: 1, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// Upsert same name again — must update, not error.
	if err := q.UpsertTargetValue(ctx, UpsertTargetValueParams{
		TargetID: tgt.ID, VarName: "API_KEY", ValueEnc: []byte{9}, IsSecret: 1, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	vals, err := q.ListTargetValues(ctx, tgt.ID)
	if err != nil || len(vals) != 1 || len(vals[0].ValueEnc) != 1 {
		t.Fatalf("vals = %+v, err = %v", vals, err)
	}

	run, err := q.CreateRun(ctx, CreateRunParams{
		ID: "r1", TargetID: sqlNullString(tgt.ID), Deployment: "prod", TargetName: "client-a", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertRunStep(ctx, InsertRunStepParams{RunID: run.ID, Idx: 0, Name: "Deploy"}); err != nil {
		t.Fatal(err)
	}
	if err := q.AppendRunLog(ctx, AppendRunLogParams{RunID: run.ID, Seq: 1, Line: "hello", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	logs, err := q.ListRunLogsAfter(ctx, ListRunLogsAfterParams{RunID: run.ID, Seq: 0})
	if err != nil || len(logs) != 1 || logs[0].Line != "hello" {
		t.Fatalf("logs = %+v, err = %v", logs, err)
	}

	// Cascade: deleting the target keeps the run (SET NULL) and removes values.
	if err := q.DeleteTarget(ctx, tgt.ID); err != nil {
		t.Fatal(err)
	}
	vals, _ = q.ListTargetValues(ctx, tgt.ID)
	if len(vals) != 0 {
		t.Fatalf("values not cascaded: %+v", vals)
	}
	got, err := q.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetID.Valid {
		t.Fatalf("run target_id should be NULL after target delete: %+v", got.TargetID)
	}
}
