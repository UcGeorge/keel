package devserver

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/smart-minds/keel/internal/config"
	"github.com/smart-minds/keel/internal/engine"
	"github.com/smart-minds/keel/internal/runhub"
	"github.com/smart-minds/keel/internal/store/devdb"
	"github.com/smart-minds/keel/internal/web"
)

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// handleDeploy validates the target and starts a run.
func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	_, d := s.dep(w, r)
	if d == nil {
		return
	}
	t := s.target(w, r, d)
	if t == nil {
		return
	}

	values, _, err := s.targetValues(r.Context(), d, t.ID)
	if err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Could not decrypt saved values: "+err.Error())
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
		s.renderTarget(w, r, d, t, nil, msgs, dstate, http.StatusUnprocessableEntity)
		return
	}
	if err := s.Runner.CheckDocker(r.Context()); err != nil {
		s.renderTarget(w, r, d, t, nil, []string{err.Error()}, nil, http.StatusServiceUnavailable)
		return
	}
	values = merged

	runID := uuid.NewString()
	now := time.Now().UnixMilli()
	run, err := s.Q.CreateRun(r.Context(), devdb.CreateRunParams{
		ID: runID, TargetID: nullStr(t.ID), Deployment: d.Name, TargetName: t.Name, CreatedAt: now,
	})
	if err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Could not create run: "+err.Error())
		return
	}
	for i, step := range d.Steps {
		if err := s.Q.InsertRunStep(r.Context(), devdb.InsertRunStepParams{RunID: runID, Idx: int64(i), Name: step.Name}); err != nil {
			s.errorPage(w, r, http.StatusInternalServerError, "Could not create run: "+err.Error())
			return
		}
	}

	s.startRun(run, d, t, values)
	http.Redirect(w, r, "/runs/"+runID, http.StatusSeeOther)
}

// collectDeployValues reads the deploy modal's variable values from the
// deploy form. Absent or blank fields are simply not set — defaults and
// requiredness are applied by CheckValues/ResolveValues.
func collectDeployValues(r *http.Request, d *config.Deployment) map[string]string {
	_ = r.ParseForm()
	out := map[string]string{}
	for _, v := range d.DeployTimeVariables() {
		raw, present := formValue(r, v.Name)
		if !present {
			continue
		}
		if v.Type != config.VarMultiline {
			raw = strings.TrimSpace(raw)
		}
		if raw != "" {
			out[v.Name] = raw
		}
	}
	return out
}

// startRun launches the engine in the background for an already-created run.
func (s *Server) startRun(run *devdb.Run, d *config.Deployment, t *devdb.Target, values map[string]string) {
	runID := run.ID
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
	spec := engine.Spec{
		RunID:        runID,
		Deployment:   d.Name,
		Target:       t.Name,
		RepoDir:      s.RepoDir,
		Dockerfile:   d.Environment.Dockerfile,
		Context:      d.Environment.Context,
		Steps:        steps,
		Env:          env,
		SecretValues: secrets,
		Outputs:      d.OutputNames(),
		ImageTag:     "keel/dev-" + d.Name,
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
		sink := &devSink{s: s, runID: runID}
		res, err := s.Runner.Run(ctx, spec, sink)

		status := "succeeded"
		errMsg := ""
		switch {
		case res.Canceled:
			status = "canceled"
			errMsg = "Run canceled."
		case err != nil:
			status = "failed"
			errMsg = err.Error()
		}
		bg := context.Background()
		if status == "succeeded" {
			s.storeRunOutputs(bg, runID, d, res.Outputs, secrets)
		}
		if err := s.Q.FinishRun(bg, devdb.FinishRunParams{
			Status:     status,
			ExitCode:   sql.NullInt64{Int64: int64(res.ExitCode), Valid: res.ExitCode >= 0},
			FailedStep: sql.NullInt64{Int64: int64(res.FailedStep), Valid: res.FailedStep >= 0},
			Error:      errMsg,
			FinishedAt: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true},
			ID:         runID,
		}); err != nil {
			slog.Error("finish run", "run", runID, "err", err)
		}
		if err := s.Q.SkipUnfinishedRunSteps(bg, runID); err != nil {
			slog.Error("skip unfinished steps", "run", runID, "err", err)
		}
		s.Hub.End(runID, status)
	}()
}

// storeRunOutputs encrypts and persists the outputs captured from a
// successful run. An output whose value matches a secret input is treated
// as secret even without the flag, so a re-exported credential never
// renders in the clear.
func (s *Server) storeRunOutputs(ctx context.Context, runID string, d *config.Deployment, outputs map[string]string, secretValues []string) {
	now := time.Now().UnixMilli()
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
		if err := s.Q.InsertRunOutput(ctx, devdb.InsertRunOutputParams{
			RunID: runID, Name: name, ValueEnc: enc, IsSecret: boolInt(secret), CreatedAt: now,
		}); err != nil {
			slog.Error("store run output", "run", runID, "output", name, "err", err)
		}
	}
}

// runOutputVMs loads, decrypts, and orders a run's outputs for display.
// d may be nil (deployment no longer in the config).
func (s *Server) runOutputVMs(ctx context.Context, runID string, d *config.Deployment, succeeded bool) []web.OutputVM {
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
		stored[row.Name] = web.StoredOutput{Value: value, Secret: row.IsSecret != 0}
	}
	if len(stored) == 0 && !succeeded {
		return nil // failed runs have no outputs; skip the "not set" noise
	}
	if len(stored) == 0 && (d == nil || len(d.Outputs) == 0) {
		return nil
	}
	return web.BuildOutputVMs(d, stored, true)
}

// devSink persists engine output and republishes it to the hub.
type devSink struct {
	s     *Server
	runID string
	seq   int64
}

func (k *devSink) Log(line string) {
	k.seq++
	if err := k.s.Q.AppendRunLog(context.Background(), devdb.AppendRunLogParams{
		RunID: k.runID, Seq: k.seq, Line: line, CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		slog.Error("append run log", "run", k.runID, "err", err)
	}
	k.s.Hub.Publish(k.runID, runhub.Event{Kind: runhub.EventLog, Seq: k.seq, Line: line})
}

func (k *devSink) Phase(p engine.Phase) {
	status := string(p)
	ctx := context.Background()
	var err error
	if p == engine.PhaseBuilding {
		err = k.s.Q.StartRun(ctx, devdb.StartRunParams{
			Status: status, StartedAt: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}, ID: k.runID,
		})
	} else {
		err = k.s.Q.SetRunStatus(ctx, devdb.SetRunStatusParams{Status: status, ID: k.runID})
	}
	if err != nil {
		slog.Error("set run status", "run", k.runID, "err", err)
	}
	k.s.Hub.Publish(k.runID, runhub.Event{Kind: runhub.EventStatus, Status: status})
}

func (k *devSink) StepStatus(idx int, status engine.StepStatus) {
	if err := k.s.Q.SetRunStepStatus(context.Background(), devdb.SetRunStepStatusParams{
		Status: string(status), RunID: k.runID, Idx: int64(idx),
	}); err != nil {
		slog.Error("set step status", "run", k.runID, "err", err)
	}
	k.s.Hub.Publish(k.runID, runhub.Event{Kind: runhub.EventStep, StepIdx: idx, StepStatus: string(status)})
}

// handleRun renders the run page.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Q.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		s.errorPage(w, r, http.StatusNotFound, "Run not found")
		return
	}
	vm := runVM(run)
	steps := s.stepVMs(r.Context(), run.ID)
	logs, _ := s.Q.ListRunLogs(r.Context(), run.ID)
	var d *config.Deployment
	if cfg, _, _ := s.loadConfig(); cfg != nil {
		d = cfg.Deployment(run.Deployment)
	}
	page := web.PageRun{
		Base:      s.base(w, r, "Run"),
		Run:       vm,
		Steps:     steps,
		Outputs:   s.runOutputVMs(r.Context(), run.ID, d, run.Status == "succeeded"),
		CanCancel: true,
		Live:      vm.Active,
		BackURL:   backURLForRun(run),
	}
	for _, l := range logs {
		page.LogLines = append(page.LogLines, web.LogLineVM{Seq: l.Seq, Line: l.Line})
		page.LastSeq = l.Seq
	}
	page.EventsURL = fmt.Sprintf("/runs/%s/events", run.ID)
	s.Renderer.Render(w, http.StatusOK, "run.html", page)
}

func backURLForRun(run *devdb.Run) string {
	if run.TargetID.Valid {
		return targetURL(run.Deployment, run.TargetName)
	}
	return "/runs"
}

func (s *Server) stepVMs(ctx context.Context, runID string) []web.StepVM {
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

// handleRunCancel aborts an active run.
func (s *Server) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, err := s.Q.GetRun(r.Context(), runID); err != nil {
		s.errorPage(w, r, http.StatusNotFound, "Run not found")
		return
	}
	s.mu.Lock()
	cancel := s.cancels[runID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		web.SetFlash(w, "info", "Run canceled.")
	} else {
		web.SetFlash(w, "info", "This run is no longer active.")
	}
	http.Redirect(w, r, "/runs/"+runID, http.StatusSeeOther)
}

// handleRunEvents streams run updates over SSE.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	run, err := s.Q.GetRun(r.Context(), runID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

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

	// Local run state for rendering meta fragments as events arrive.
	vm := runVM(run)
	steps := s.stepVMs(r.Context(), runID)

	send := s.metaSender(sse, &vm, steps)

	replay, ch, cancel, active := s.Hub.Subscribe(runID, afterSeq)
	if !active {
		// The run finished between page load and stream attach: emit the
		// missed log lines, the final meta, and close.
		logs, _ := s.Q.ListRunLogsAfter(r.Context(), devdb.ListRunLogsAfterParams{RunID: runID, Seq: afterSeq})
		for _, l := range logs {
			_ = sse.Event(strconv.FormatInt(l.Seq, 10), "log", logLineHTML(l.Line))
		}
		run, err := s.Q.GetRun(r.Context(), runID)
		if err == nil {
			vm = runVM(run)
			send()
		}
		_ = sse.Event("", "done", "closed")
		return
	}
	defer cancel()

	apply := func(ev runhub.Event) bool {
		switch ev.Kind {
		case runhub.EventLog:
			return sse.Event(strconv.FormatInt(ev.Seq, 10), "log", logLineHTML(ev.Line)) == nil
		case runhub.EventStep:
			if ev.StepIdx >= 0 && ev.StepIdx < len(steps) {
				steps[ev.StepIdx].Status = ev.StepStatus
			}
			return send()
		case runhub.EventStatus:
			vm.Status = ev.Status
			applyStatusTimes(&vm)
			return send()
		case runhub.EventDone:
			vm.Status = ev.Status
			vm.Active = false
			finishRunVM(&vm)
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

// metaSender renders and sends the run_meta fragment from current state.
func (s *Server) metaSender(sse *web.SSE, vm *web.RunVM, steps []web.StepVM) func() bool {
	return func() bool {
		html, err := s.Renderer.FragmentHTML("run.html", "run_meta", map[string]any{
			"Run": *vm, "Steps": steps, "CanCancel": true, "CSRF": s.csrf,
		})
		if err != nil {
			slog.Error("render run meta", "err", err)
			return true
		}
		return sse.Event("", "meta", html) == nil
	}
}

func applyStatusTimes(vm *web.RunVM) {
	if vm.StartedAt == nil && (vm.Status == "building" || vm.Status == "running") {
		now := time.Now()
		vm.StartedAt = &now
	}
	vm.Active = vm.Status == "queued" || vm.Status == "building" || vm.Status == "running"
}

func finishRunVM(vm *web.RunVM) {
	if vm.FinishedAt == nil {
		now := time.Now()
		vm.FinishedAt = &now
	}
}

// logLineHTML formats one log line as the HTML appended to the log pane.
func logLineHTML(line string) string { return web.LogLineHTML(line) }
