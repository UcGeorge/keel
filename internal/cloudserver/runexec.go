package cloudserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/smart-minds/keel/internal/config"
	"github.com/smart-minds/keel/internal/engine"
	"github.com/smart-minds/keel/internal/gitutil"
	"github.com/smart-minds/keel/internal/runhub"
	"github.com/smart-minds/keel/internal/store/clouddb"
	"github.com/smart-minds/keel/internal/web"
)

// handleDeploy validates the target and starts a run.
func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.canDeploy() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "You don't have permission to deploy this target")
		return
	}
	d := s.repoDep(w, r, rc)
	if d == nil {
		return
	}
	t := s.pathTarget(w, r, rc, d)
	if t == nil {
		return
	}
	values, _, err := s.targetValues(r.Context(), d, t.ID)
	if err != nil {
		s.errorPage(w, r, rc.Sess, http.StatusInternalServerError, "Could not decrypt saved values: "+err.Error())
		return
	}
	deployVals := collectDeployValues(r, d)
	merged := mergedValues(values, deployVals)
	if problems := config.CheckValues(d, merged); len(problems) > 0 {
		// Problems on deploy-time variables re-open the modal; anything else
		// is a configuration problem shown in the target's banner.
		deployErrors := map[string]string{}
		var msgs []string
		for _, p := range problems {
			if v := d.Variable(p.Name); v != nil && v.DeployTime {
				deployErrors[p.Name] = p.Message
			} else {
				msgs = append(msgs, p.Name+" "+p.Message)
			}
		}
		var dstate *deployFormState
		if len(msgs) == 0 {
			dstate = &deployFormState{Values: deployVals, Errors: deployErrors}
		}
		s.renderTarget(w, r, rc, d, t, nil, msgs, dstate, http.StatusUnprocessableEntity)
		return
	}
	values = merged
	if active, err := s.Q.CountActiveRunsForTarget(r.Context(), nullUUID(t.ID)); err == nil && active > 0 {
		s.renderTarget(w, r, rc, d, t, nil, []string{"A run is already in progress for this target — wait for it to finish or cancel it."}, nil, http.StatusConflict)
		return
	}
	if err := s.Runner.CheckDocker(r.Context()); err != nil {
		s.renderTarget(w, r, rc, d, t, nil, []string{err.Error()}, nil, http.StatusServiceUnavailable)
		return
	}

	run, err := s.createRun(r.Context(), rc.Repo, d, t, "manual", "", nullUUID(rc.Sess.UserID))
	if err != nil {
		s.errorPage(w, r, rc.Sess, http.StatusInternalServerError, "Could not create the run: "+err.Error())
		return
	}
	s.startRun(run, rc.Repo, d, t.Name, values)
	http.Redirect(w, r, rc.repoURL()+"/runs/"+run.ID.String(), http.StatusSeeOther)
}

// collectDeployValues reads the deploy modal's variable values from the
// deploy form. Absent or blank fields are simply not set — defaults and
// requiredness are applied by CheckValues/ResolveValues.
func collectDeployValues(r *http.Request, d *config.Deployment) map[string]string {
	_ = r.ParseForm()
	out := map[string]string{}
	for _, v := range d.DeployTimeVariables() {
		vs, present := r.PostForm[v.Name]
		if !present || len(vs) == 0 {
			continue
		}
		raw := vs[0]
		if v.Type != config.VarMultiline {
			raw = strings.TrimSpace(raw)
		}
		if raw != "" {
			out[v.Name] = raw
		}
	}
	return out
}

// autoDeploy starts a push-triggered run when the target is fully
// configured. Deploy-time variables fall back to their defaults; a required
// one without a default blocks the auto-deploy with a clear message.
func (s *Server) autoDeploy(ctx context.Context, repo *clouddb.Repo, d *config.Deployment, t *clouddb.Target, commitSHA string) error {
	values, _, err := s.targetValues(ctx, d, t.ID)
	if err != nil {
		return err
	}
	if problems := config.CheckValues(d, values); len(problems) > 0 {
		names := make([]string, len(problems))
		for i, p := range problems {
			names[i] = p.Name + " " + p.Message
		}
		return fmt.Errorf("target %q is not ready: %s", t.Name, strings.Join(names, "; "))
	}
	if active, err := s.Q.CountActiveRunsForTarget(ctx, nullUUID(t.ID)); err == nil && active > 0 {
		return fmt.Errorf("target %q already has an active run", t.Name)
	}
	run, err := s.createRun(ctx, repo, d, t, "push", commitSHA, uuid.NullUUID{})
	if err != nil {
		return err
	}
	s.startRun(run, repo, d, t.Name, values)
	return nil
}

// createRun inserts the run and its step rows.
func (s *Server) createRun(ctx context.Context, repo *clouddb.Repo, d *config.Deployment, t *clouddb.Target, trigger, commitSHA string, startedBy uuid.NullUUID) (*clouddb.Run, error) {
	run, err := s.Q.CreateRun(ctx, clouddb.CreateRunParams{
		RepoID: repo.ID, TargetID: nullUUID(t.ID), Deployment: d.Name, TargetName: t.Name,
		TriggerKind: trigger, CommitSha: commitSHA, StartedBy: startedBy,
	})
	if err != nil {
		return nil, err
	}
	for i, step := range d.Steps {
		if err := s.Q.InsertRunStep(ctx, clouddb.InsertRunStepParams{RunID: run.ID, Idx: int32(i), Name: step.Name}); err != nil {
			return nil, err
		}
	}
	return run, nil
}

// startRun clones the repository and executes the deployment in background.
func (s *Server) startRun(run *clouddb.Run, repo *clouddb.Repo, d *config.Deployment, targetName string, values map[string]string) {
	runID := run.ID.String()
	env := config.ResolveValues(d, values)
	var secrets []string
	for _, v := range d.Variables {
		if v.Secret && env[v.Name] != "" {
			secrets = append(secrets, env[v.Name])
		}
	}
	steps := make([]engine.Step, len(d.Steps))
	for i, st := range d.Steps {
		steps[i] = engine.Step{Name: st.Name, Run: st.Run}
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[runID] = cancel
	s.mu.Unlock()
	s.Hub.Begin(runID)

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.cancels, runID)
			s.mu.Unlock()
			cancel()
		}()
		sink := &cloudSink{s: s, runID: run.ID}
		bg := context.Background()

		finish := func(status, errMsg string, res engine.Result) {
			if err := s.Q.FinishRun(bg, clouddb.FinishRunParams{
				ID: run.ID, Status: status,
				ExitCode:   pgInt4(res.ExitCode, res.ExitCode >= 0),
				FailedStep: pgInt4(res.FailedStep, res.FailedStep >= 0),
				Error:      errMsg,
			}); err != nil {
				slog.Error("finish run", "run", runID, "err", err)
			}
			if err := s.Q.SkipUnfinishedRunSteps(bg, run.ID); err != nil {
				slog.Error("skip unfinished steps", "run", runID, "err", err)
			}
			s.Hub.End(runID, status)
		}

		// Phase 0: clone the repository at the configured branch.
		workdir := filepath.Join(s.Cfg.DataDir, "runs", runID)
		defer os.RemoveAll(workdir)
		sink.Log(fmt.Sprintf("=> Cloning %s (branch %s)", repo.GitUrl, repo.Branch))
		auth, err := s.repoAuth(repo)
		if err != nil {
			sink.Log("clone failed: " + err.Error())
			finish("failed", "clone failed: "+err.Error(), engine.Result{ExitCode: -1, FailedStep: -1})
			return
		}
		sha, err := gitutil.CloneShallow(ctx, repo.GitUrl, repo.Branch, workdir, auth)
		if err != nil {
			status, msg := "failed", err.Error()
			if ctx.Err() != nil {
				status, msg = "canceled", "Run canceled."
			}
			sink.Log(msg)
			finish(status, msg, engine.Result{ExitCode: -1, FailedStep: -1})
			return
		}
		sink.Log(fmt.Sprintf("=> Checked out %.7s", sha))
		if run.CommitSha == "" {
			_ = s.Q.SetRunCommit(bg, clouddb.SetRunCommitParams{ID: run.ID, CommitSha: sha})
		}

		spec := engine.Spec{
			RunID:        runID,
			Deployment:   d.Name,
			Target:       targetName,
			RepoDir:      workdir,
			Dockerfile:   d.Environment.Dockerfile,
			Context:      d.Environment.Context,
			Steps:        steps,
			Env:          env,
			SecretValues: secrets,
			Outputs:      d.OutputNames(),
			ImageTag:     "keel/cloud-" + run.RepoID.String()[:8] + "-" + d.Name,
		}
		res, err := s.Runner.Run(ctx, spec, sink)
		switch {
		case res.Canceled:
			finish("canceled", "Run canceled.", res)
		case err != nil:
			finish("failed", err.Error(), res)
		default:
			s.storeRunOutputs(bg, run.ID, d, res.Outputs, secrets)
			finish("succeeded", "", res)
		}
	}()
}

// storeRunOutputs encrypts and persists the outputs captured from a
// successful run. An output whose value contains a secret input is treated
// as secret even without the flag, so a re-exported credential never
// renders in the clear.
func (s *Server) storeRunOutputs(ctx context.Context, runID uuid.UUID, d *config.Deployment, outputs map[string]string, secretValues []string) {
	for name, value := range outputs {
		secret := false
		if o := d.Output(name); o != nil {
			secret = o.Secret
		}
		for _, sv := range secretValues {
			if sv != "" && strings.Contains(value, sv) {
				secret = true
				break
			}
		}
		enc, err := s.Box.SealString(value)
		if err != nil {
			slog.Error("seal run output", "run", runID, "output", name, "err", err)
			continue
		}
		if err := s.Q.InsertRunOutput(ctx, clouddb.InsertRunOutputParams{
			RunID: runID, Name: name, ValueEnc: enc, IsSecret: secret,
		}); err != nil {
			slog.Error("store run output", "run", runID, "output", name, "err", err)
		}
	}
}

// runOutputVMs loads, decrypts, and orders a run's outputs for display.
// d may be nil (deployment no longer in the synced config); canReveal
// gates whether secret values reach the page at all.
func (s *Server) runOutputVMs(ctx context.Context, runID uuid.UUID, d *config.Deployment, succeeded, canReveal bool) []web.OutputVM {
	rows, err := s.Q.ListRunOutputs(ctx, runID)
	if err != nil {
		return nil
	}
	stored := map[string]web.StoredOutput{}
	for _, row := range rows {
		value, err := s.Box.OpenString(row.ValueEnc)
		if err != nil {
			slog.Error("decrypt run output", "run", runID, "output", row.Name, "err", err)
			continue
		}
		stored[row.Name] = web.StoredOutput{Value: value, Secret: row.IsSecret}
	}
	if len(stored) == 0 && !succeeded {
		return nil // failed runs have no outputs; skip the "not set" noise
	}
	if len(stored) == 0 && (d == nil || len(d.Outputs) == 0) {
		return nil
	}
	return web.BuildOutputVMs(d, stored, canReveal)
}

func pgInt4(v int, valid bool) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(v), Valid: valid}
}

// cloudSink persists engine output and republishes it to the hub.
type cloudSink struct {
	s     *Server
	runID uuid.UUID
	seq   int64
}

func (k *cloudSink) Log(line string) {
	k.seq++
	if err := k.s.Q.AppendRunLog(context.Background(), clouddb.AppendRunLogParams{
		RunID: k.runID, Seq: int32(k.seq), Line: line,
	}); err != nil {
		slog.Error("append run log", "run", k.runID, "err", err)
	}
	k.s.Hub.Publish(k.runID.String(), runhub.Event{Kind: runhub.EventLog, Seq: k.seq, Line: line})
}

func (k *cloudSink) Phase(p engine.Phase) {
	status := string(p)
	ctx := context.Background()
	var err error
	if p == engine.PhaseBuilding {
		err = k.s.Q.StartRun(ctx, clouddb.StartRunParams{ID: k.runID, Status: status})
	} else {
		err = k.s.Q.SetRunStatus(ctx, clouddb.SetRunStatusParams{ID: k.runID, Status: status})
	}
	if err != nil {
		slog.Error("set run status", "run", k.runID, "err", err)
	}
	k.s.Hub.Publish(k.runID.String(), runhub.Event{Kind: runhub.EventStatus, Status: status})
}

func (k *cloudSink) StepStatus(idx int, status engine.StepStatus) {
	if err := k.s.Q.SetRunStepStatus(context.Background(), clouddb.SetRunStepStatusParams{
		RunID: k.runID, Idx: int32(idx), Status: string(status),
	}); err != nil {
		slog.Error("set step status", "run", k.runID, "err", err)
	}
	k.s.Hub.Publish(k.runID.String(), runhub.Event{Kind: runhub.EventStep, StepIdx: idx, StepStatus: string(status)})
}

// --- run pages ---------------------------------------------------------------

// pathRun loads the run in the path, scoped to the repository.
func (s *Server) pathRun(w http.ResponseWriter, r *http.Request, rc *repoCtx) *clouddb.Run {
	id := parseUUID(r.PathValue("id"))
	run, err := s.Q.GetRun(r.Context(), id)
	if err != nil || run.RepoID != rc.Repo.ID {
		s.errorPage(w, r, rc.Sess, http.StatusNotFound, "Run not found")
		return nil
	}
	return run
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	run := s.pathRun(w, r, rc)
	if run == nil {
		return
	}
	vm := s.runVM(rc, run, rc.Repo.Name)
	var d *config.Deployment
	if cfg := repoConfig(rc.Repo); cfg != nil {
		d = cfg.Deployment(run.Deployment)
	}
	page := web.PageRun{
		Base:      s.base(w, r, rc.Sess, rc.orgCtx, "Run"),
		Run:       vm,
		Steps:     s.stepVMs(r.Context(), run.ID),
		Outputs:   s.runOutputVMs(r.Context(), run.ID, d, run.Status == "succeeded", rc.canDeploy() || rc.canConfigure()),
		CanCancel: rc.canDeploy(),
		Live:      vm.Active,
		BackURL:   s.runBackURL(rc, run),
		EventsURL: rc.repoURL() + "/runs/" + run.ID.String() + "/events",
	}
	logs, _ := s.Q.ListRunLogs(r.Context(), run.ID)
	for _, l := range logs {
		page.LogLines = append(page.LogLines, web.LogLineVM{Seq: int64(l.Seq), Line: l.Line})
		page.LastSeq = int64(l.Seq)
	}
	s.Renderer.Render(w, http.StatusOK, "run.html", page)
}

func (s *Server) runBackURL(rc *repoCtx, run *clouddb.Run) string {
	if run.TargetID.Valid {
		return s.targetURL(rc, run.Deployment, run.TargetName)
	}
	return rc.repoURL() + "/runs"
}

func (s *Server) stepVMs(ctx context.Context, runID uuid.UUID) []web.StepVM {
	rows, err := s.Q.ListRunSteps(ctx, runID)
	if err != nil {
		return nil
	}
	steps := make([]web.StepVM, len(rows))
	for i, row := range rows {
		steps[i] = web.StepVM{Idx: int(row.Idx), Name: row.Name, Status: row.Status}
	}
	return steps
}

func (s *Server) handleRunCancel(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.canDeploy() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "You don't have permission to cancel runs")
		return
	}
	run := s.pathRun(w, r, rc)
	if run == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancels[run.ID.String()]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		web.SetFlash(w, "info", "Run canceled.")
	} else {
		web.SetFlash(w, "info", "This run is no longer active.")
	}
	http.Redirect(w, r, rc.repoURL()+"/runs/"+run.ID.String(), http.StatusSeeOther)
}

// handleRunEvents streams run updates over SSE.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	run := s.pathRun(w, r, rc)
	if run == nil {
		return
	}
	runID := run.ID.String()

	afterSeq := int64(0)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			afterSeq = n
		}
	}
	sse, err := web.NewSSE(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	vm := s.runVM(rc, run, rc.Repo.Name)
	steps := s.stepVMs(r.Context(), run.ID)
	canCancel := rc.canDeploy()
	send := func() bool {
		html, err := s.Renderer.FragmentHTML("run.html", "run_meta", map[string]any{
			"Run": vm, "Steps": steps, "CanCancel": canCancel, "CSRF": rc.Sess.CsrfToken,
		})
		if err != nil {
			slog.Error("render run meta", "err", err)
			return true
		}
		return sse.Event("", "meta", html) == nil
	}

	replay, ch, cancel, active := s.Hub.Subscribe(runID, afterSeq)
	if !active {
		logs, _ := s.Q.ListRunLogsAfter(r.Context(), clouddb.ListRunLogsAfterParams{RunID: run.ID, Seq: int32(afterSeq)})
		for _, l := range logs {
			_ = sse.Event(strconv.Itoa(int(l.Seq)), "log", web.LogLineHTML(l.Line))
		}
		if fresh, err := s.Q.GetRun(r.Context(), run.ID); err == nil {
			vm = s.runVM(rc, fresh, rc.Repo.Name)
			steps = s.stepVMs(r.Context(), run.ID)
			send()
		}
		_ = sse.Event("", "done", "closed")
		return
	}
	defer cancel()

	apply := func(ev runhub.Event) bool {
		switch ev.Kind {
		case runhub.EventLog:
			return sse.Event(strconv.FormatInt(ev.Seq, 10), "log", web.LogLineHTML(ev.Line)) == nil
		case runhub.EventStep:
			if ev.StepIdx >= 0 && ev.StepIdx < len(steps) {
				steps[ev.StepIdx].Status = ev.StepStatus
			}
			return send()
		case runhub.EventStatus:
			vm.Status = ev.Status
			if vm.StartedAt == nil && (ev.Status == "building" || ev.Status == "running") {
				now := time.Now()
				vm.StartedAt = &now
			}
			vm.Active = true
			return send()
		case runhub.EventDone:
			vm.Status = ev.Status
			vm.Active = false
			if vm.FinishedAt == nil {
				now := time.Now()
				vm.FinishedAt = &now
			}
			send()
			_ = sse.Event("", "done", "closed")
			return false
		}
		return true
	}
	for _, ev := range replay {
		if !apply(ev) {
			return
		}
	}
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if sse.Comment("keepalive") != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !apply(ev) {
				return
			}
		}
	}
}
