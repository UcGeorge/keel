package cloudserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/UcGeorge/keel/internal/config"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
	"github.com/google/uuid"
)

// repoDep resolves the deployment named in the path from the repo's synced
// configuration.
func (s *Server) repoDep(w http.ResponseWriter, r *http.Request, rc *repoCtx) *config.Deployment {
	cfg := repoConfig(rc.Repo)
	if cfg == nil {
		s.errorPage(w, r, rc.Sess, http.StatusConflict, "The repository's Keel configuration is missing or invalid — sync the repository first")
		return nil
	}
	d := cfg.Deployment(r.PathValue("dep"))
	if d == nil {
		s.errorPage(w, r, rc.Sess, http.StatusNotFound, fmt.Sprintf("No deployment named %q in this repository's keel.yaml", r.PathValue("dep")))
		return nil
	}
	return d
}

// pathTarget resolves the target named in the path.
func (s *Server) pathTarget(w http.ResponseWriter, r *http.Request, rc *repoCtx, d *config.Deployment) *clouddb.Target {
	t, err := s.Q.GetTargetByName(r.Context(), clouddb.GetTargetByNameParams{
		RepoID: rc.Repo.ID, Deployment: d.Name, Lower: r.PathValue("target"),
	})
	if err != nil {
		s.errorPage(w, r, rc.Sess, http.StatusNotFound, fmt.Sprintf("No target named %q for deployment %q", r.PathValue("target"), d.Name))
		return nil
	}
	return t
}

func (s *Server) depURL(rc *repoCtx, dep string) string {
	return rc.repoURL() + "/deployments/" + dep
}

func (s *Server) targetURL(rc *repoCtx, dep, target string) string {
	return s.depURL(rc, dep) + "/targets/" + target
}

// targetValues decrypts saved values for declared variables.
func (s *Server) targetValues(ctx context.Context, d *config.Deployment, targetID uuid.UUID) (map[string]string, map[string]bool, error) {
	rows, err := s.Q.ListTargetValues(ctx, targetID)
	if err != nil {
		return nil, nil, err
	}
	values := map[string]string{}
	savedSecrets := map[string]bool{}
	for _, row := range rows {
		v := d.Variable(row.VarName)
		if v == nil {
			continue
		}
		plain, err := s.Box.OpenString(row.ValueEnc)
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt %s: %w", row.VarName, err)
		}
		values[row.VarName] = plain
		if v.Secret {
			savedSecrets[row.VarName] = true
		}
	}
	return values, savedSecrets, nil
}

// targetVM builds the target row view model.
func (s *Server) targetVM(ctx context.Context, rc *repoCtx, d *config.Deployment, t *clouddb.Target, withLastRun bool) web.TargetVM {
	vm := web.TargetVM{
		ID:          t.ID.String(),
		Deployment:  d.Name,
		Name:        t.Name,
		Description: t.Description,
		AutoDeploy:  t.AutoDeploy,
		URL:         s.targetURL(rc, d.Name, t.Name),
		Editable:    rc.canConfigure(),
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
		runs, err := s.Q.ListRunsForTarget(ctx, clouddb.ListRunsForTargetParams{
			TargetID: nullUUID(t.ID), Limit: 1,
		})
		if err == nil && len(runs) == 1 {
			lr := s.runVM(rc, runs[0], "")
			vm.LastRun = &lr
		}
	}
	return vm
}

// runVM converts a stored run to its view model.
func (s *Server) runVM(rc *repoCtx, run *clouddb.Run, repoName string) web.RunVM {
	base := rc.repoURL()
	vm := web.RunVM{
		ID:         run.ID.String(),
		Deployment: run.Deployment,
		TargetName: run.TargetName,
		RepoName:   repoName,
		Status:     run.Status,
		Trigger:    run.TriggerKind,
		CommitSHA:  run.CommitSha,
		Error:      run.Error,
		CreatedAt:  run.CreatedAt,
		StartedAt:  run.StartedAt,
		FinishedAt: run.FinishedAt,
		URL:        base + "/runs/" + run.ID.String(),
		CancelURL:  base + "/runs/" + run.ID.String() + "/cancel",
	}
	if run.ExitCode.Valid {
		v := int(run.ExitCode.Int32)
		vm.ExitCode = &v
	}
	if run.FailedStep.Valid {
		v := int(run.FailedStep.Int32)
		vm.FailedStep = &v
	}
	if run.StartedBy.Valid {
		if u, err := s.Q.GetUser(context.Background(), run.StartedBy.UUID); err == nil {
			vm.StartedBy = u.Name
		}
	}
	vm.Active = run.Status == "queued" || run.Status == "building" || run.Status == "running"
	return vm
}

// handleDeployment renders one deployment with its targets.
func (s *Server) handleDeployment(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	d := s.repoDep(w, r, rc)
	if d == nil {
		return
	}
	s.renderDeployment(w, r, rc, d, &web.TargetFormVM{
		Action:   s.depURL(rc, d.Name) + "/targets",
		Submit:   "Create target",
		ShowAuto: rc.Repo.Provider == "github_app",
	}, http.StatusOK)
}

func (s *Server) renderDeployment(w http.ResponseWriter, r *http.Request, rc *repoCtx, d *config.Deployment, form *web.TargetFormVM, code int) {
	vm := web.NewDeploymentVM(d, s.depURL(rc, d.Name))
	targets, err := s.Q.ListTargetsForDeployment(r.Context(), clouddb.ListTargetsForDeploymentParams{
		RepoID: rc.Repo.ID, Deployment: d.Name,
	})
	if err == nil {
		for _, t := range targets {
			vm.Targets = append(vm.Targets, s.targetVM(r.Context(), rc, d, t, true))
		}
	}
	page := web.PageDeployment{
		Base:         s.base(w, r, rc.Sess, rc.orgCtx, d.Name),
		Dep:          vm,
		CanConfigure: rc.canConfigure(),
		CanDeploy:    rc.canDeploy(),
		ShowAuto:     rc.Repo.Provider == "github_app",
		TargetForm:   form,
		BackURL:      rc.repoURL(),
		BackLabel:    rc.Repo.Name,
	}
	s.Renderer.Render(w, code, "deployment.html", page)
}

// handleTargetCreate creates a deployment target.
func (s *Server) handleTargetCreate(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.canConfigure() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "You don't have permission to create targets")
		return
	}
	d := s.repoDep(w, r, rc)
	if d == nil {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	desc := strings.TrimSpace(r.PostFormValue("description"))
	autoDeploy := r.PostFormValue("auto_deploy") == "true" && rc.Repo.Provider == "github_app"
	form := &web.TargetFormVM{
		Action: s.depURL(rc, d.Name) + "/targets", Submit: "Create target",
		Name: name, Description: desc, AutoDeploy: autoDeploy,
		ShowAuto: rc.Repo.Provider == "github_app",
	}
	if !config.ValidTargetName(name) {
		form.NameError = "Use lowercase letters, digits, and hyphens (e.g. \"client-acme\")."
		s.renderDeployment(w, r, rc, d, form, http.StatusUnprocessableEntity)
		return
	}
	_, err := s.Q.CreateTarget(r.Context(), clouddb.CreateTargetParams{
		RepoID: rc.Repo.ID, Deployment: d.Name, Name: name, Description: desc,
		AutoDeploy: autoDeploy, CreatedBy: nullUUID(rc.Sess.UserID),
	})
	if err != nil {
		form.NameError = fmt.Sprintf("A target named %q already exists for this deployment.", name)
		s.renderDeployment(w, r, rc, d, form, http.StatusUnprocessableEntity)
		return
	}
	web.SetFlash(w, "success", fmt.Sprintf("Target %q created — set its variables below.", name))
	http.Redirect(w, r, s.targetURL(rc, d.Name, name), http.StatusSeeOther)
}

// handleTarget renders the target page.
func (s *Server) handleTarget(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	d := s.repoDep(w, r, rc)
	if d == nil {
		return
	}
	t := s.pathTarget(w, r, rc, d)
	if t == nil {
		return
	}
	// ?deploy=1 (the deployment page's Deploy shortcut) opens the deploy
	// modal immediately when there are deploy-time variables to ask for.
	var dstate *deployFormState
	if r.URL.Query().Get("deploy") == "1" && len(d.DeployTimeVariables()) > 0 {
		dstate = &deployFormState{}
	}
	s.renderTarget(w, r, rc, d, t, nil, nil, nil, dstate, http.StatusOK)
}

// deployFormState carries the deploy modal's submitted values and errors
// through a re-render after a failed deploy attempt.
type deployFormState struct {
	Values map[string]string
	Errors map[string]string
}

func (s *Server) renderTarget(w http.ResponseWriter, r *http.Request, rc *repoCtx, d *config.Deployment, t *clouddb.Target, fieldErrors map[string]string, submitted map[string]string, problems []string, dstate *deployFormState, code int) {
	values, savedSecrets, err := s.targetValues(r.Context(), d, t.ID)
	if err != nil {
		s.errorPage(w, r, rc.Sess, http.StatusInternalServerError, "Could not decrypt saved values: "+err.Error())
		return
	}
	tURL := s.targetURL(rc, d.Name, t.Name)
	// After a failed save, submitted overlays the stored values so nothing
	// the user typed is lost — only the invalid fields need correcting.
	fields := web.BuildVarFields(d, d.ConfigVariables(), mergedValues(values, submitted), savedSecrets, fieldErrors)
	deployValues := values
	deployErrors := map[string]string{}
	if dstate != nil {
		deployValues = mergedValues(values, dstate.Values)
		deployErrors = dstate.Errors
	}
	deployFields := web.BuildVarFields(d, d.DeployTimeVariables(), deployValues, nil, deployErrors)
	page := web.PageTarget{
		Base:         s.base(w, r, rc.Sess, rc.orgCtx, d.Name+" / "+t.Name),
		Dep:          web.NewDeploymentVM(d, s.depURL(rc, d.Name)),
		Target:       s.targetVM(r.Context(), rc, d, t, false),
		Fields:       fields,
		Layout:       web.BuildVarLayout(d, fields),
		DeployFields: deployFields,
		DeployLayout: web.BuildVarLayout(d, deployFields),
		DeployOpen:   dstate != nil,
		Runs:         s.targetRunsTable(r, rc, t, tURL+"/runs-fragment"),
		CanConfigure: rc.canConfigure(),
		CanDeploy:    rc.canDeploy(),
		SaveURL:      tURL + "/values",
		DeployURL:    tURL + "/deploy",
		DeleteURL:    tURL + "/delete",
		ManifestURL:  tURL + "/manifest",
		EditForm: &web.TargetFormVM{
			Action: tURL + "/update", Submit: "Save settings",
			Name: t.Name, Description: t.Description,
			AutoDeploy: t.AutoDeploy, ShowAuto: rc.Repo.Provider == "github_app",
		},
		ShowAuto: rc.Repo.Provider == "github_app",
		Problems: problems,
		BackURL:  s.depURL(rc, d.Name),
	}
	if lr, err := s.Q.LatestSucceededRunForTarget(r.Context(), nullUUID(t.ID)); err == nil {
		outs := s.runOutputVMs(r.Context(), lr.ID, d, true, rc.canDeploy() || rc.canConfigure())
		for _, o := range outs {
			if o.Set {
				vm := s.runVM(rc, lr, "")
				page.LatestOutputs = outs
				page.LatestOutputsRun = &vm
				break
			}
		}
	}
	s.Renderer.Render(w, code, "target.html", page)
}

func (s *Server) targetRunsTable(r *http.Request, rc *repoCtx, t *clouddb.Target, pollURL string) web.RunsTableVM {
	table := web.RunsTableVM{PollURL: pollURL}
	runs, err := s.Q.ListRunsForTarget(r.Context(), clouddb.ListRunsForTargetParams{TargetID: nullUUID(t.ID), Limit: 50})
	if err != nil {
		return table
	}
	for _, run := range runs {
		vm := s.runVM(rc, run, "")
		table.Runs = append(table.Runs, vm)
		if vm.Active {
			table.Poll = true
		}
	}
	return table
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

// handleValuesSave validates and persists variable values.
func (s *Server) handleValuesSave(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.canConfigure() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "You don't have permission to configure this target")
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
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, rc.Sess, http.StatusBadRequest, "Invalid form submission")
		return
	}

	fieldErrors := map[string]string{}
	submitted := map[string]string{}
	type pending struct {
		name   string
		value  string
		secret bool
	}
	var updates []pending
	for _, v := range d.ConfigVariables() {
		vs, present := r.PostForm[v.Name]
		if !present || len(vs) == 0 {
			continue
		}
		value := vs[0]
		if v.Type != config.VarMultiline {
			value = strings.TrimSpace(value)
		}
		if !v.Secret {
			submitted[v.Name] = value
		}
		if v.Secret && strings.TrimSpace(value) == "" {
			continue // blank secret means "keep the saved one"
		}
		if value == "" {
			updates = append(updates, pending{name: v.Name})
			continue
		}
		if msg := config.CheckValue(v, value); msg != "" {
			fieldErrors[v.Name] = msg
			continue
		}
		updates = append(updates, pending{name: v.Name, value: value, secret: v.Secret})
	}
	for _, u := range updates {
		if u.value == "" {
			if err := s.Q.DeleteTargetValue(r.Context(), clouddb.DeleteTargetValueParams{TargetID: t.ID, VarName: u.name}); err != nil {
				s.errorPage(w, r, rc.Sess, http.StatusInternalServerError, "Could not save values")
				return
			}
			continue
		}
		enc, err := s.Box.SealString(u.value)
		if err != nil {
			s.errorPage(w, r, rc.Sess, http.StatusInternalServerError, "Could not encrypt value")
			return
		}
		if err := s.Q.UpsertTargetValue(r.Context(), clouddb.UpsertTargetValueParams{
			TargetID: t.ID, VarName: u.name, ValueEnc: enc, IsSecret: u.secret,
			UpdatedBy: nullUUID(rc.Sess.UserID),
		}); err != nil {
			s.errorPage(w, r, rc.Sess, http.StatusInternalServerError, "Could not save values")
			return
		}
	}
	// Valid values are persisted even when some fields fail — the re-rendered
	// form keeps everything the user typed and flags only the invalid fields.
	if len(fieldErrors) > 0 {
		s.renderTarget(w, r, rc, d, t, fieldErrors, submitted, nil, nil, http.StatusUnprocessableEntity)
		return
	}
	web.SetFlash(w, "success", "Variables saved.")
	http.Redirect(w, r, s.targetURL(rc, d.Name, t.Name), http.StatusSeeOther)
}

// handleTargetUpdate updates target settings.
func (s *Server) handleTargetUpdate(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.canConfigure() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "You don't have permission to configure this target")
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
	name := strings.TrimSpace(r.PostFormValue("name"))
	desc := strings.TrimSpace(r.PostFormValue("description"))
	autoDeploy := r.PostFormValue("auto_deploy") == "true" && rc.Repo.Provider == "github_app"
	if !config.ValidTargetName(name) {
		web.SetFlash(w, "error", "Invalid target name — use lowercase letters, digits, and hyphens.")
		http.Redirect(w, r, s.targetURL(rc, d.Name, t.Name), http.StatusSeeOther)
		return
	}
	if _, err := s.Q.UpdateTarget(r.Context(), clouddb.UpdateTargetParams{
		ID: t.ID, Name: name, Description: desc, AutoDeploy: autoDeploy,
	}); err != nil {
		web.SetFlash(w, "error", fmt.Sprintf("A target named %q already exists.", name))
		http.Redirect(w, r, s.targetURL(rc, d.Name, t.Name), http.StatusSeeOther)
		return
	}
	web.SetFlash(w, "success", "Target updated.")
	http.Redirect(w, r, s.targetURL(rc, d.Name, name), http.StatusSeeOther)
}

// handleTargetDelete removes a target; runs are kept.
func (s *Server) handleTargetDelete(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	if !rc.canConfigure() {
		s.errorPage(w, r, rc.Sess, http.StatusForbidden, "You don't have permission to delete this target")
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
	if err := s.Q.DeleteTarget(r.Context(), t.ID); err != nil {
		s.errorPage(w, r, rc.Sess, http.StatusInternalServerError, "Could not delete the target")
		return
	}
	web.SetFlash(w, "success", fmt.Sprintf("Target %q deleted.", t.Name))
	http.Redirect(w, r, s.depURL(rc, d.Name), http.StatusSeeOther)
}

// handleTargetRunsFragment serves the polled per-target runs table.
func (s *Server) handleTargetRunsFragment(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	d := s.repoDep(w, r, rc)
	if d == nil {
		return
	}
	t := s.pathTarget(w, r, rc, d)
	if t == nil {
		return
	}
	table := s.targetRunsTable(r, rc, t, s.targetURL(rc, d.Name, t.Name)+"/runs-fragment")
	s.Renderer.RenderFragment(w, "runs.html", "runs_table", table)
}

// handleManifest renders the manifest builder and downloads.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	d := s.repoDep(w, r, rc)
	if d == nil {
		return
	}
	targetName := ""
	backURL := s.depURL(rc, d.Name)
	action := s.depURL(rc, d.Name) + "/manifest"
	if r.PathValue("target") != "" {
		t := s.pathTarget(w, r, rc, d)
		if t == nil {
			return
		}
		targetName = t.Name
		backURL = s.targetURL(rc, d.Name, t.Name)
		action = s.targetURL(rc, d.Name, t.Name) + "/manifest"
	}
	web.ServeManifestBuilder(s.Renderer, w, r, web.ManifestRequest{
		Base:       s.base(w, r, rc.Sess, rc.orgCtx, "Variable manifest"),
		Dep:        d,
		DepURL:     s.depURL(rc, d.Name),
		TargetName: targetName,
		Project:    rc.Org.Name + " / " + rc.Repo.Name,
		PreparedBy: rc.Sess.User.Name,
		FormAction: action,
		BackURL:    backURL,
	})
}

// --- runs pages --------------------------------------------------------------

func (s *Server) repoRunsTable(r *http.Request, rc *repoCtx) web.RunsTableVM {
	table := web.RunsTableVM{ShowTarget: true, PollURL: rc.repoURL() + "/runs-fragment"}
	runs, err := s.Q.ListRunsForRepo(r.Context(), clouddb.ListRunsForRepoParams{RepoID: rc.Repo.ID, Limit: 100})
	if err != nil {
		return table
	}
	for _, run := range runs {
		vm := s.runVM(rc, run, "")
		table.Runs = append(table.Runs, vm)
		if vm.Active {
			table.Poll = true
		}
	}
	return table
}

func (s *Server) handleRepoRuns(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	page := web.PageRuns{
		Base:    s.base(w, r, rc.Sess, rc.orgCtx, rc.Repo.Name+" runs"),
		Heading: rc.Repo.Name + " — runs",
		Table:   s.repoRunsTable(r, rc),
	}
	s.Renderer.Render(w, http.StatusOK, "runs.html", page)
}

func (s *Server) handleRepoRunsFragment(w http.ResponseWriter, r *http.Request, rc *repoCtx) {
	s.Renderer.RenderFragment(w, "runs.html", "runs_table", s.repoRunsTable(r, rc))
}

func (s *Server) orgRunsTable(r *http.Request, oc *orgCtx) web.RunsTableVM {
	table := web.RunsTableVM{ShowTarget: true, ShowRepo: true, PollURL: oc.urlBase() + "/runs-fragment"}
	runs, err := s.Q.ListRunsForOrg(r.Context(), clouddb.ListRunsForOrgParams{OrgID: oc.Org.ID, Limit: 100})
	if err != nil {
		return table
	}
	for _, row := range runs {
		run := &clouddb.Run{
			ID: row.ID, RepoID: row.RepoID, TargetID: row.TargetID,
			Deployment: row.Deployment, TargetName: row.TargetName,
			Status: row.Status, TriggerKind: row.TriggerKind, CommitSha: row.CommitSha,
			ExitCode: row.ExitCode, FailedStep: row.FailedStep, Error: row.Error,
			StartedBy: row.StartedBy, CreatedAt: row.CreatedAt,
			StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		}
		fakeRepo := &repoCtx{orgCtx: oc, Repo: &clouddb.Repo{Name: row.RepoName, OrgID: oc.Org.ID}}
		vm := s.runVM(fakeRepo, run, row.RepoName)
		table.Runs = append(table.Runs, vm)
		if vm.Active {
			table.Poll = true
		}
	}
	return table
}

func (s *Server) handleOrgRuns(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	page := web.PageRuns{
		Base:    s.base(w, r, oc.Sess, oc, "Runs"),
		Heading: "Runs",
		Table:   s.orgRunsTable(r, oc),
	}
	s.Renderer.Render(w, http.StatusOK, "runs.html", page)
}

func (s *Server) handleOrgRunsFragment(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
	s.Renderer.RenderFragment(w, "runs.html", "runs_table", s.orgRunsTable(r, oc))
}
