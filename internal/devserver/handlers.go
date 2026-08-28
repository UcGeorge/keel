package devserver

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/smart-minds/keel/internal/config"
	"github.com/smart-minds/keel/internal/store/devdb"
	"github.com/smart-minds/keel/internal/web"
)

// handleDashboard renders the deployments overview.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	cfg, status, cfgPath := s.loadConfig()
	page := web.PageDashboard{
		Base:       s.base(w, r, "Deployments"),
		Config:     status,
		ConfigPath: cfgPath,
	}
	if cfg != nil && status.OK {
		for _, d := range cfg.Deployments {
			vm := web.NewDeploymentVM(d, depURL(d.Name))
			targets, err := s.Q.ListTargetsByDeployment(r.Context(), d.Name)
			if err == nil {
				for _, t := range targets {
					vm.Targets = append(vm.Targets, s.targetVM(r.Context(), d, t, false))
				}
			}
			page.Deployments = append(page.Deployments, vm)
		}
	}
	s.Renderer.Render(w, http.StatusOK, "dashboard.html", page)
}

// handleConfig renders the raw configuration with validation state.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	_, status, cfgPath := s.loadConfig()
	raw := ""
	if data, err := readRepoFile(cfgPath); err == nil {
		raw = data
	}
	page := web.PageConfig{
		Base:    s.base(w, r, "Configuration"),
		Config:  status,
		RawYAML: raw,
		Path:    cfgPath,
	}
	s.Renderer.Render(w, http.StatusOK, "config.html", page)
}

func readRepoFile(path string) (string, error) {
	data, err := osReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// targetVM builds the target row view, optionally including the last run.
func (s *Server) targetVM(ctx context.Context, d *config.Deployment, t *devdb.Target, withLastRun bool) web.TargetVM {
	vm := web.TargetVM{
		ID:          t.ID,
		Deployment:  d.Name,
		Name:        t.Name,
		Description: t.Description,
		URL:         targetURL(d.Name, t.Name),
		Editable:    true,
	}
	values, _, err := s.targetValues(ctx, d, t.ID)
	if err == nil {
		for _, v := range d.ConfigVariables() {
			vm.VarsTotal++
			if values[v.Name] != "" {
				vm.VarsSet++
			}
		}
		problems := config.CheckConfigValues(d, values)
		vm.Ready = len(problems) == 0
		vm.Missing = len(problems)
	}
	if withLastRun {
		runs, err := s.Q.ListRunsByTarget(ctx, devdb.ListRunsByTargetParams{
			TargetID: nullStr(t.ID), Limit: 1,
		})
		if err == nil && len(runs) == 1 {
			lr := runVM(runs[0])
			vm.LastRun = &lr
		}
	}
	return vm
}

// handleDeployment renders one deployment with its targets.
func (s *Server) handleDeployment(w http.ResponseWriter, r *http.Request) {
	_, d := s.dep(w, r)
	if d == nil {
		return
	}
	s.renderDeployment(w, r, d, &web.TargetFormVM{
		Action: depURL(d.Name) + "/targets",
		Submit: "Create target",
	}, http.StatusOK)
}

func (s *Server) renderDeployment(w http.ResponseWriter, r *http.Request, d *config.Deployment, form *web.TargetFormVM, code int) {
	vm := web.NewDeploymentVM(d, depURL(d.Name))
	targets, err := s.Q.ListTargetsByDeployment(r.Context(), d.Name)
	if err == nil {
		for _, t := range targets {
			vm.Targets = append(vm.Targets, s.targetVM(r.Context(), d, t, true))
		}
	}
	page := web.PageDeployment{
		Base:         s.base(w, r, d.Name),
		Dep:          vm,
		CanConfigure: true,
		TargetForm:   form,
		BackURL:      "/",
		BackLabel:    "Deployments",
	}
	s.Renderer.Render(w, code, "deployment.html", page)
}

// handleTargetCreate creates a deployment target.
func (s *Server) handleTargetCreate(w http.ResponseWriter, r *http.Request) {
	_, d := s.dep(w, r)
	if d == nil {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	desc := strings.TrimSpace(r.PostFormValue("description"))
	form := &web.TargetFormVM{
		Action: depURL(d.Name) + "/targets", Submit: "Create target",
		Name: name, Description: desc,
	}
	if !config.ValidTargetName(name) {
		form.NameError = "Use lowercase letters, digits, and hyphens (e.g. \"client-acme\")."
		s.renderDeployment(w, r, d, form, http.StatusUnprocessableEntity)
		return
	}
	now := time.Now().UnixMilli()
	_, err := s.Q.CreateTarget(r.Context(), devdb.CreateTargetParams{
		ID: uuid.NewString(), Deployment: d.Name, Name: name, Description: desc,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		form.NameError = fmt.Sprintf("A target named %q already exists for this deployment.", name)
		s.renderDeployment(w, r, d, form, http.StatusUnprocessableEntity)
		return
	}
	web.SetFlash(w, "success", fmt.Sprintf("Target %q created — set its variables below.", name))
	http.Redirect(w, r, targetURL(d.Name, name), http.StatusSeeOther)
}

// handleTarget renders the target page: variables form, deploy, runs.
func (s *Server) handleTarget(w http.ResponseWriter, r *http.Request) {
	_, d := s.dep(w, r)
	if d == nil {
		return
	}
	t := s.target(w, r, d)
	if t == nil {
		return
	}
	s.renderTarget(w, r, d, t, nil, nil, nil, http.StatusOK)
}

// deployFormState carries the deploy modal's submitted values and errors
// through a re-render after a failed deploy attempt.
type deployFormState struct {
	Values map[string]string
	Errors map[string]string
}

func (s *Server) renderTarget(w http.ResponseWriter, r *http.Request, d *config.Deployment, t *devdb.Target, fieldErrors map[string]string, problems []string, dstate *deployFormState, code int) {
	values, savedSecrets, err := s.targetValues(r.Context(), d, t.ID)
	if err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Could not decrypt saved values: "+err.Error())
		return
	}
	tURL := targetURL(d.Name, t.Name)
	runs := s.runsTable(r.Context(), t, tURL+"/runs-fragment")
	fields := web.BuildVarFields(d, d.ConfigVariables(), values, savedSecrets, fieldErrors)
	deployValues := values
	deployErrors := map[string]string{}
	if dstate != nil {
		deployValues = mergedValues(values, dstate.Values)
		deployErrors = dstate.Errors
	}
	deployFields := web.BuildVarFields(d, d.DeployTimeVariables(), deployValues, nil, deployErrors)
	page := web.PageTarget{
		Base:         s.base(w, r, d.Name+" / "+t.Name),
		Dep:          web.NewDeploymentVM(d, depURL(d.Name)),
		Target:       s.targetVM(r.Context(), d, t, false),
		Fields:       fields,
		Layout:       web.BuildVarLayout(d, fields),
		DeployFields: deployFields,
		DeployLayout: web.BuildVarLayout(d, deployFields),
		DeployOpen:   dstate != nil,
		Runs:         runs,
		CanConfigure: true,
		CanDeploy:    true,
		SaveURL:      tURL + "/values",
		DeployURL:    tURL + "/deploy",
		DeleteURL:    tURL + "/delete",
		ManifestURL:  tURL + "/manifest",
		EditForm: &web.TargetFormVM{
			Action: tURL + "/update", Submit: "Save settings",
			Name: t.Name, Description: t.Description,
		},
		Problems: problems,
		BackURL:  depURL(d.Name),
	}
	if lr, err := s.Q.LatestSucceededRunForTarget(r.Context(), nullStr(t.ID)); err == nil {
		outs := s.runOutputVMs(r.Context(), lr.ID, d, true)
		for _, o := range outs {
			if o.Set {
				vm := runVM(lr)
				page.LatestOutputs = outs
				page.LatestOutputsRun = &vm
				break
			}
		}
	}
	s.Renderer.Render(w, code, "target.html", page)
}

func (s *Server) runsTable(ctx context.Context, t *devdb.Target, pollURL string) web.RunsTableVM {
	table := web.RunsTableVM{ShowTarget: false, PollURL: pollURL}
	runs, err := s.Q.ListRunsByTarget(ctx, devdb.ListRunsByTargetParams{TargetID: nullStr(t.ID), Limit: 50})
	if err != nil {
		return table
	}
	for _, run := range runs {
		vm := runVM(run)
		table.Runs = append(table.Runs, vm)
		if vm.Active {
			table.Poll = true
		}
	}
	return table
}

// handleValuesSave validates and persists variable values for a target.
func (s *Server) handleValuesSave(w http.ResponseWriter, r *http.Request) {
	_, d := s.dep(w, r)
	if d == nil {
		return
	}
	t := s.target(w, r, d)
	if t == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "Invalid form submission")
		return
	}

	fieldErrors := map[string]string{}
	type pending struct {
		name   string
		value  string
		secret bool
	}
	var updates []pending
	for _, v := range d.ConfigVariables() {
		raw, present := formValue(r, v.Name)
		if !present {
			continue
		}
		value := raw
		if v.Type != config.VarMultiline {
			value = strings.TrimSpace(value)
		}
		if value == "" {
			if v.Secret {
				continue // blank secret input means "keep the saved one"
			}
			updates = append(updates, pending{name: v.Name, value: "", secret: false})
			continue
		}
		if msg := config.CheckValue(v, value); msg != "" {
			fieldErrors[v.Name] = msg
			continue
		}
		updates = append(updates, pending{name: v.Name, value: value, secret: v.Secret})
	}

	if len(fieldErrors) > 0 {
		s.renderTarget(w, r, d, t, fieldErrors, nil, nil, http.StatusUnprocessableEntity)
		return
	}

	now := time.Now().UnixMilli()
	for _, u := range updates {
		if u.value == "" {
			if err := s.Q.DeleteTargetValue(r.Context(), devdb.DeleteTargetValueParams{TargetID: t.ID, VarName: u.name}); err != nil {
				s.errorPage(w, r, http.StatusInternalServerError, "Could not save values: "+err.Error())
				return
			}
			continue
		}
		enc, err := s.Box.SealString(u.value)
		if err != nil {
			s.errorPage(w, r, http.StatusInternalServerError, "Could not encrypt value: "+err.Error())
			return
		}
		if err := s.Q.UpsertTargetValue(r.Context(), devdb.UpsertTargetValueParams{
			TargetID: t.ID, VarName: u.name, ValueEnc: enc, IsSecret: boolInt(u.secret), UpdatedAt: now,
		}); err != nil {
			s.errorPage(w, r, http.StatusInternalServerError, "Could not save values: "+err.Error())
			return
		}
	}
	web.SetFlash(w, "success", "Variables saved.")
	http.Redirect(w, r, targetURL(d.Name, t.Name), http.StatusSeeOther)
}

// mergedValues overlays extra on top of base without mutating either.
func mergedValues(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// formValue returns a form value and whether the field was submitted at all.
func formValue(r *http.Request, name string) (string, bool) {
	vs, ok := r.PostForm[name]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

// handleTargetUpdate renames a target or updates its description.
func (s *Server) handleTargetUpdate(w http.ResponseWriter, r *http.Request) {
	_, d := s.dep(w, r)
	if d == nil {
		return
	}
	t := s.target(w, r, d)
	if t == nil {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	desc := strings.TrimSpace(r.PostFormValue("description"))
	if !config.ValidTargetName(name) {
		web.SetFlash(w, "error", "Invalid target name — use lowercase letters, digits, and hyphens.")
		http.Redirect(w, r, targetURL(d.Name, t.Name), http.StatusSeeOther)
		return
	}
	if _, err := s.Q.UpdateTarget(r.Context(), devdb.UpdateTargetParams{
		Name: name, Description: desc, UpdatedAt: time.Now().UnixMilli(), ID: t.ID,
	}); err != nil {
		web.SetFlash(w, "error", fmt.Sprintf("A target named %q already exists.", name))
		http.Redirect(w, r, targetURL(d.Name, t.Name), http.StatusSeeOther)
		return
	}
	web.SetFlash(w, "success", "Target updated.")
	http.Redirect(w, r, targetURL(d.Name, name), http.StatusSeeOther)
}

// handleTargetDelete removes a target and its values; runs are kept.
func (s *Server) handleTargetDelete(w http.ResponseWriter, r *http.Request) {
	_, d := s.dep(w, r)
	if d == nil {
		return
	}
	t := s.target(w, r, d)
	if t == nil {
		return
	}
	if err := s.Q.DeleteTarget(r.Context(), t.ID); err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Could not delete target: "+err.Error())
		return
	}
	web.SetFlash(w, "success", fmt.Sprintf("Target %q deleted.", t.Name))
	http.Redirect(w, r, depURL(d.Name), http.StatusSeeOther)
}

// handleRuns renders the global runs page.
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	table := s.allRunsTable(r)
	page := web.PageRuns{
		Base:    s.base(w, r, "Runs"),
		Heading: "Runs",
		Table:   table,
	}
	s.Renderer.Render(w, http.StatusOK, "runs.html", page)
}

func (s *Server) allRunsTable(r *http.Request) web.RunsTableVM {
	table := web.RunsTableVM{ShowTarget: true, PollURL: "/runs-fragment"}
	runs, err := s.Q.ListRuns(r.Context(), 100)
	if err != nil {
		return table
	}
	for _, run := range runs {
		vm := runVM(run)
		table.Runs = append(table.Runs, vm)
		if vm.Active {
			table.Poll = true
		}
	}
	return table
}

// handleRunsFragment serves the polled runs-table fragment (htmx).
func (s *Server) handleRunsFragment(w http.ResponseWriter, r *http.Request) {
	var table web.RunsTableVM
	if depName := r.PathValue("dep"); depName != "" {
		cfg, status, _ := s.loadConfig()
		if cfg == nil || !status.OK {
			http.Error(w, "config unavailable", http.StatusConflict)
			return
		}
		d := cfg.Deployment(depName)
		if d == nil {
			http.NotFound(w, r)
			return
		}
		t := s.target(w, r, d)
		if t == nil {
			return
		}
		table = s.runsTable(r.Context(), t, targetURL(d.Name, t.Name)+"/runs-fragment")
	} else {
		table = s.allRunsTable(r)
	}
	s.Renderer.RenderFragment(w, "runs.html", "runs_table", table)
}

// handleManifest renders the manifest builder and serves downloads.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	_, d := s.dep(w, r)
	if d == nil {
		return
	}
	targetName := ""
	backURL := depURL(d.Name)
	action := depURL(d.Name) + "/manifest"
	if r.PathValue("target") != "" {
		t := s.target(w, r, d)
		if t == nil {
			return
		}
		targetName = t.Name
		backURL = targetURL(d.Name, t.Name)
		action = targetURL(d.Name, t.Name) + "/manifest"
	}
	s.serveManifestBuilder(w, r, manifestReq{
		dep: d, targetName: targetName, backURL: backURL, action: action,
		project: filepath.Base(s.RepoDir),
	})
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
