package web

import (
	"html/template"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/UcGeorge/keel/internal/config"
)

const sampleYAML = `
version: 1
deployments:
  prod:
    description: Deploys the API to production.
    long_description: |
      # Production deploy

      Ships the API to **production** via the release pipeline.
    groups:
      1: Service settings
      2: {label: Tuning, description: "Optional knobs.", collapsed: true}
    environment: {dockerfile: deploy/Dockerfile}
    steps:
      - {name: Authenticate, run: ./auth.sh}
      - {name: Deploy, run: ./deploy.sh}
    variables:
      API_KEY:
        label: API Key
        secret: true
        description: The service key.
        manifest: {why: Authenticates the deploy., how: Create one in the dashboard.}
      REGION:
        type: select
        default: us
        options: [{value: us, label: United States}, eu]
        group: 1
        row: 1
        flex: 2
      REPLICAS:
        type: number
        required: false
        validation: {min: 1, max: 10}
        group: 1
        row: 1
      DEBUG:
        type: boolean
        required: false
        group: 2
      NOTES:
        type: multiline
        required: false
        group: 1
      ACTION:
        type: select
        deploy_time: true
        default: deploy
        options: [deploy, destroy]
      CONFIRM:
        deploy_time: true
        when: {var: ACTION, eq: destroy}
        validation: {pattern: destroy, message: Type "destroy" to confirm.}
    outputs:
      SERVICE_URL:
        label: Service URL
        description: Where the deployed API answers.
      ADMIN_TOKEN: {secret: true}
      NEVER_SET: {}
`

func sampleDep(t *testing.T) *config.Deployment {
	t.Helper()
	cfg, err := config.Parse([]byte(sampleYAML))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Deployment("prod")
}

func sampleBase() Base {
	return Base{
		Title: "Test", AppName: "Keel", Mode: "dev",
		Nav:         []NavItem{{Label: "Deployments", Href: "/", Active: true}, {Label: "Runs", Href: "/runs"}},
		ContextName: "/tmp/repo",
		Flash:       &Flash{Kind: "success", Message: "Saved."},
		CSRF:        "csrftoken",
		Version:     "test",
	}
}

func cloudBase() Base {
	b := sampleBase()
	b.AppName = "Keel Cloud"
	b.Mode = "cloud"
	b.User = &UserInfo{Name: "Pete George", Email: "pete@example.com"}
	b.Orgs = []OrgNavItem{{Name: "Pete George", Slug: "pete", Personal: true, Active: true}, {Name: "Acme", Slug: "acme"}}
	b.OrgSlug = "pete"
	return b
}

func sampleRun(active bool) RunVM {
	started := time.Now().Add(-90 * time.Second)
	vm := RunVM{
		ID: "0193b2fa-1111-2222-3333-444455556666", Deployment: "prod", TargetName: "client-a",
		RepoName: "api", Status: "running", Trigger: "manual", StartedBy: "Pete",
		CreatedAt: time.Now().Add(-2 * time.Minute), StartedAt: &started,
		URL: "/runs/x", CancelURL: "/runs/x/cancel", Active: active,
	}
	if !active {
		finished := time.Now()
		vm.Status = "failed"
		vm.FinishedAt = &finished
		vm.Error = "step \"Deploy\" failed with exit code 3"
		code := 3
		step := 1
		vm.ExitCode = &code
		vm.FailedStep = &step
	}
	return vm
}

func sampleTable() RunsTableVM {
	return RunsTableVM{
		Runs: []RunVM{sampleRun(true), sampleRun(false)}, ShowTarget: true, ShowRepo: true,
		Poll: true, PollURL: "/runs-fragment",
	}
}

func sampleDepVM(t *testing.T) *DeploymentVM {
	d := sampleDep(t)
	vm := NewDeploymentVM(d, "/deployments/prod")
	lr := sampleRun(false)
	vm.Targets = []TargetVM{{
		ID: "t1", Deployment: "prod", Name: "client-a", Description: "Acme", AutoDeploy: true,
		URL: "/deployments/prod/targets/client-a", VarsSet: 2, VarsTotal: 5, Ready: false,
		LastRun: &lr, Editable: true,
	}}
	return vm
}

// TestRenderAllPages executes every page template with representative data;
// a template/data mismatch fails here instead of in production.
func TestRenderAllPages(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	d := sampleDep(t)
	values := map[string]string{"REGION": "us"}
	fields := BuildVarFields(d, d.ConfigVariables(), values, map[string]bool{"API_KEY": true}, map[string]string{"REPLICAS": "must be a number"})
	deployFields := BuildVarFields(d, d.DeployTimeVariables(), values, nil, map[string]string{"CONFIRM": "Type \"destroy\" to confirm."})
	outputs := BuildOutputVMs(d, map[string]StoredOutput{
		"SERVICE_URL": {Value: "https://api.example.com"},
		"ADMIN_TOKEN": {Value: "tok-123", Secret: true},
		"LEGACY_OUT":  {Value: "kept"},
	}, true)
	form := &TargetFormVM{Action: "/x", Submit: "Create target", ShowAuto: true}
	now := time.Now()

	pages := map[string]any{
		"dashboard.html": PageDashboard{
			Base: sampleBase(), Config: ConfigStatusVM{OK: true, Source: "keel.yaml"},
			Deployments: []*DeploymentVM{sampleDepVM(t)}, ConfigPath: "/tmp/repo/keel.yaml",
		},
		"deployment.html": PageDeployment{
			Base: sampleBase(), Dep: sampleDepVM(t), CanConfigure: true, ShowAuto: true,
			TargetForm: form, BackURL: "/", BackLabel: "Deployments",
		},
		"target.html": PageTarget{
			Base: sampleBase(), Dep: sampleDepVM(t),
			Target: sampleDepVM(t).Targets[0], Fields: fields, Layout: BuildVarLayout(d, fields),
			DeployFields: deployFields, DeployLayout: BuildVarLayout(d, deployFields), DeployOpen: true,
			LatestOutputs: outputs, LatestOutputsRun: sampleDepVM(t).Targets[0].LastRun,
			Runs:         sampleTable(),
			CanConfigure: true, CanDeploy: true,
			SaveURL: "/s", DeployURL: "/d", DeleteURL: "/del", ManifestURL: "/m",
			EditForm: form, ShowAuto: true, Problems: []string{"API_KEY is required"}, BackURL: "/x",
		},
		"run.html": PageRun{
			Base: sampleBase(), Run: sampleRun(true),
			Steps:    []StepVM{{0, "Authenticate", "succeeded"}, {1, "Deploy", "running"}},
			Inputs:   BuildRunInputsVM(sampleInputs()),
			Insight:  &InsightCardVM{URL: "/runs/x/insight", CSRF: "csrftoken", Insight: &InsightVM{Content: template.HTML("<h2>What happened</h2>"), Model: "gpt-4o-mini", CreatedAt: now, CreatedBy: "Pete"}},
			Outputs:  outputs,
			LogLines: []LogLineVM{{1, "=> Building environment image"}, {2, "done"}},
			LastSeq:  2, EventsURL: "/runs/x/events", CanCancel: true, Live: true, BackURL: "/x",
		},
		"runs.html": PageRuns{Base: sampleBase(), Heading: "Runs", Table: sampleTable()},
		"manifest.html": PageManifest{
			Base: sampleBase(), Dep: sampleDepVM(t), TargetName: "client-a",
			Selected: map[string]bool{"API_KEY": true}, Preview: MarkdownHTML("# Required values\n\nBody."),
			FormAction: "/m", BackURL: "/x",
		},
		"config.html": PageConfig{
			Base: sampleBase(),
			Config: ConfigStatusVM{Errors: []config.ValidationError{
				{Path: "deployments.prod.steps", Message: "at least one step is required"},
			}, Source: "keel.yaml"},
			RawYAML: sampleYAML, Path: "/tmp/repo/keel.yaml",
		},
		"error.html": PageError{Base: sampleBase(), Code: 404, Message: "Page not found", HomeURL: "/"},

		"auth/login.html":  PageLogin{Base: Base{AppName: "Keel Cloud", Mode: "cloud", CSRF: "t"}, Email: "a@b.co", Error: "Incorrect email or password.", Next: "/x"},
		"auth/signup.html": PageSignup{Base: Base{AppName: "Keel Cloud", Mode: "cloud", CSRF: "t"}, Name: "P", Email: "a@b.co", Next: "/x", Errors: map[string]string{"password": "too short"}},
		"auth/invite.html": PageInvite{Base: Base{AppName: "Keel Cloud", Mode: "cloud", CSRF: "t"}, OrgName: "Acme", Role: "admin", Token: "tok", NeedsAuth: true},

		"cloud/org_new.html": PageOrgNew{Base: cloudBase(), Name: "Acme", Error: "taken"},
		"cloud/repos.html": PageRepos{
			Base: cloudBase(), CanConfigure: true, ConnectURL: "/orgs/pete/repos/new",
			Repos: []RepoVM{
				{ID: "r1", Name: "api", Provider: "git", GitURL: "https://x/y.git", Branch: "main", Status: "ok", URL: "/orgs/pete/repos/api", Deployments: 2, LastSyncedAt: &now},
				{ID: "r2", Name: "web", Provider: "github_app", GithubFullName: "acme/web", Branch: "main", Status: "config_invalid", ConfigError: "bad", URL: "/orgs/pete/repos/web"},
			},
		},
		"cloud/repo_new.html": PageRepoNew{
			Base: cloudBase(), Branch: "main", Errors: map[string]string{"git_url": "bad url"},
			GithubEnabled: true, GithubRepos: []GithubPickVM{{FullName: "acme/api", InstallationID: 1}},
			GithubError: "rate limited", InstallURL: "https://github.com/apps/keel/installations/new", FormAction: "/orgs/pete/repos",
		},
		"cloud/repo.html": PageRepo{
			Base:   cloudBase(),
			Repo:   RepoVM{ID: "r1", Name: "api", Provider: "git", GitURL: "https://x/y.git", Branch: "main", Status: "ok", URL: "/orgs/pete/repos/api", LastSyncedAt: &now, LastCommitSHA: "abcdef1234567890"},
			Config: ConfigStatusVM{OK: true, Source: "main @ abcdef1"}, Deployments: []*DeploymentVM{sampleDepVM(t)},
			CanConfigure: true, SyncURL: "/orgs/pete/repos/api/sync",
		},
		"cloud/repo_settings.html": PageRepoSettings{
			Base:   cloudBase(),
			Repo:   RepoVM{ID: "r1", Name: "api", Provider: "git", GitURL: "https://x/y.git", Branch: "main", URL: "/orgs/pete/repos/api"},
			Errors: map[string]string{}, HasToken: true, DeleteURL: "/orgs/pete/repos/api/delete",
		},
		"cloud/members.html": PageMembers{
			Base:      cloudBase(),
			CanManage: true, IsOwner: true, InviteLink: "https://keel.example/invites/tok", Error: "",
			Members: []MemberVM{
				{UserID: "u1", Name: "Pete George", Email: "pete@example.com", Role: "owner", IsSelf: true},
				{UserID: "u2", Name: "Ada", Email: "ada@example.com", Role: "member", CanConfigure: true, Editable: true},
			},
			Invites: []InviteVM{{ID: "i1", Email: "new@example.com", Role: "member", ExpiresAt: time.Now().Add(24 * time.Hour)}},
		},
		"cloud/org_settings.html": PageOrgSettings{Base: cloudBase(), OrgName: "Acme", Personal: false, IsOwner: true},
		"cloud/ai.html": PageAI{
			Base: cloudBase(), URLBase: "/orgs/pete", Configured: true, BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini", HasKey: true, VerifiedAt: &now,
			Presets:     []AIPresetVM{{Name: "OpenAI", BaseURL: "https://api.openai.com/v1"}},
			ModelPicker: AIModelsVM{Models: []string{"gpt-4o-mini"}, Current: "gpt-4o-mini", Hidden: 3},
			TestResult:  AITestVM{OK: true, Model: "gpt-4o-mini", Reply: "OK"}, SaveEnabled: true,
		},
		"cloud/account.html": PageAccount{Base: cloudBase(), Name: "Pete George", Email: "pete@example.com", Errors: map[string]string{}},
	}

	if len(pages) != len(r.pages) {
		var missing []string
		for name := range r.pages {
			if _, ok := pages[name]; !ok {
				missing = append(missing, name)
			}
		}
		t.Errorf("render test covers %d pages but renderer has %d; untested: %v", len(pages), len(r.pages), missing)
	}

	for page, data := range pages {
		t.Run(page, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.Render(rec, 200, page, data)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, "</html>") {
				t.Fatalf("truncated output:\n%.500s", body)
			}
		})
	}
}

func sampleInputs() []RunInputVM {
	return []RunInputVM{
		{Name: "ACTION", Label: "Action", DeployTime: true, Source: InputDeploy, Value: "destroy"},
		{Name: "API_KEY", Label: "API Key", Secret: true, Source: InputSaved},
		{Name: "REPLICAS", Label: "Replicas", Source: InputUnset},
		{Name: "CONFIRM", Label: "Confirm", DeployTime: true, Source: InputInactive},
	}
}

// TestRunInputsAndInsightRender checks what the run page says about each
// kind of input and about a stored insight.
func TestRunInputsAndInsightRender(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	page := PageRun{
		Base: cloudBase(), Run: sampleRun(false), Inputs: BuildRunInputsVM(sampleInputs()),
		Insight: &InsightCardVM{URL: "/x/insight", CSRF: "x", Insight: &InsightVM{Content: template.HTML("<h2>What happened</h2>"), Model: "m", CreatedAt: now, Auto: true}},
	}
	rec := httptest.NewRecorder()
	r.Render(rec, 200, "run.html", page)
	body := rec.Body.String()
	for _, want := range []string{"Chosen when the deploy started", ">destroy<", "••••••••", "not set", "its condition did not hold", "AI insight", "What happened", "Regenerate", "generated for the failure email"} {
		if !strings.Contains(body, want) {
			t.Errorf("run page missing %q", want)
		}
	}
	if strings.Contains(body, "Explain this failure") {
		t.Error("stored insight should offer Regenerate, not Explain")
	}

	table := RunsTableVM{ShowTarget: true, Runs: []RunVM{{ID: "1", Deployment: "prod", TargetName: "a", Status: "succeeded", CreatedAt: now, Inputs: []RunInputChip{{Name: "ACTION", Value: "plan"}}}}}
	rec = httptest.NewRecorder()
	r.RenderFragment(rec, "runs.html", "runs_table", table)
	if !strings.Contains(rec.Body.String(), "ACTION=") {
		t.Error("runs table missing input chips")
	}

	rec = httptest.NewRecorder()
	r.RenderFragment(rec, "cloud/ai.html", "ai_test", AITestVM{OK: true, Model: "m", Reply: "OK"})
	if body := rec.Body.String(); !strings.Contains(body, "hx-swap-oob") || strings.Contains(body, "disabled") {
		t.Errorf("ai_test fragment should enable the save button out of band:\n%s", body)
	}
	rec = httptest.NewRecorder()
	r.RenderFragment(rec, "cloud/ai.html", "ai_models", AIModelsVM{Models: []string{"a", "b"}, Current: "b", Hidden: 1})
	if body := rec.Body.String(); !strings.Contains(body, `<option value="b" selected>`) || !strings.Contains(body, "1 that cannot chat") {
		t.Errorf("ai_models fragment wrong:\n%s", body)
	}
}

func TestRenderFragmentRunsTable(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.RenderFragment(rec, "runs.html", "runs_table", sampleTable())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "client-a") {
		t.Fatalf("fragment render failed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunMetaFragment(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	html, err := r.FragmentHTML("run.html", "run_meta", map[string]any{
		"Run": sampleRun(true), "Steps": []StepVM{{0, "A", "running"}}, "CanCancel": true, "CSRF": "tok",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Cancel run", "running", "tok"} {
		if !strings.Contains(html, want) {
			t.Errorf("run_meta missing %q", want)
		}
	}
}

func TestFlashRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	SetFlash(rec, "success", "It worked: 100%|done")
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	f := TakeFlash(rec2, req)
	if f == nil || f.Kind != "success" || f.Message != "It worked: 100%|done" {
		t.Fatalf("flash = %+v", f)
	}
}

func TestLogLineHTMLEscapes(t *testing.T) {
	out := LogLineHTML(`<script>alert(1)</script>`)
	if strings.Contains(out, "<script>") {
		t.Fatalf("unescaped: %s", out)
	}
}

// TestBuildVarLayout locks in the layout ordering rules: ungrouped
// variables first, groups ascending by ID; within each, rows ascending by
// row ID (same-row variables side by side), rowless variables last and
// full width.
func TestBuildVarLayout(t *testing.T) {
	cfg, err := config.Parse([]byte(`
version: 1
deployments:
  a:
    groups:
      5: Five
      2: Two
    environment: {dockerfile: D}
    steps: [{name: x, run: y}]
    variables:
      LOOSE_LATE: {group: 2}
      G5_ROW1: {group: 5, row: 1}
      TOP_ROW2: {row: 2}
      G2_ROW3_B: {group: 2, row: 3}
      TOP_LOOSE: {}
      G2_ROW1: {group: 2, row: 1, flex: 3}
      TOP_ROW2_B: {row: 2, flex: 2}
      G2_ROW3_A: {group: 2, row: 3}
`))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Deployment("a")
	layout := BuildVarLayout(d, BuildVarFields(d, d.Variables, nil, nil, nil))

	names := func(r VarRowVM) []string {
		var out []string
		for _, f := range r.Fields {
			out = append(out, f.V.Name)
		}
		return out
	}

	// Ungrouped: row 2 (two fields, document order), then the loose one.
	if len(layout.Ungrouped) != 2 {
		t.Fatalf("ungrouped rows = %d", len(layout.Ungrouped))
	}
	if got := names(layout.Ungrouped[0]); !reflect.DeepEqual(got, []string{"TOP_ROW2", "TOP_ROW2_B"}) || layout.Ungrouped[0].Full {
		t.Errorf("ungrouped[0] = %v (full=%v)", got, layout.Ungrouped[0].Full)
	}
	if got := names(layout.Ungrouped[1]); !reflect.DeepEqual(got, []string{"TOP_LOOSE"}) || !layout.Ungrouped[1].Full {
		t.Errorf("ungrouped[1] = %v (full=%v)", got, layout.Ungrouped[1].Full)
	}

	// Groups ascend by ID regardless of declaration order.
	if len(layout.Groups) != 2 || layout.Groups[0].ID != 2 || layout.Groups[1].ID != 5 {
		t.Fatalf("group order = %+v", layout.Groups)
	}
	g2 := layout.Groups[0]
	if g2.Label != "Two" || g2.Count != 4 {
		t.Errorf("group 2 label=%q count=%d", g2.Label, g2.Count)
	}
	// Group 2: row 1, row 3 (two side-by-side), then the rowless variable.
	if len(g2.Rows) != 3 {
		t.Fatalf("group 2 rows = %d", len(g2.Rows))
	}
	if got := names(g2.Rows[0]); !reflect.DeepEqual(got, []string{"G2_ROW1"}) {
		t.Errorf("g2 rows[0] = %v", got)
	}
	if got := names(g2.Rows[1]); !reflect.DeepEqual(got, []string{"G2_ROW3_B", "G2_ROW3_A"}) {
		t.Errorf("g2 rows[1] = %v", got)
	}
	if got := names(g2.Rows[2]); !reflect.DeepEqual(got, []string{"LOOSE_LATE"}) || !g2.Rows[2].Full {
		t.Errorf("g2 rows[2] = %v (full=%v)", got, g2.Rows[2].Full)
	}
	// A group referenced without a definition would get a fallback label;
	// group 5 has one.
	if layout.Groups[1].Label != "Five" || layout.Groups[1].Count != 1 {
		t.Errorf("group 5 = %+v", layout.Groups[1])
	}
	// Flex flows through to the effective share used by the template.
	if f := d.Variable("G2_ROW1").EffectiveFlex(); f != 3 {
		t.Errorf("G2_ROW1 flex = %v", f)
	}
}

// TestBuildOutputVMs locks in output ordering, secrecy inheritance, and
// the reveal permission gate.
func TestBuildOutputVMs(t *testing.T) {
	d := sampleDep(t) // declares SERVICE_URL, ADMIN_TOKEN (secret), NEVER_SET
	stored := map[string]StoredOutput{
		"SERVICE_URL": {Value: "https://api.example.com"},
		"ADMIN_TOKEN": {Value: "tok-123", Secret: true},
		"AUTO_SECRET": {Value: "leaked-cred", Secret: true}, // undeclared, flagged at capture
		"LEGACY_OUT":  {Value: "kept"},
	}

	vms := BuildOutputVMs(d, stored, true)
	byName := map[string]OutputVM{}
	var order []string
	for _, vm := range vms {
		byName[vm.Name] = vm
		order = append(order, vm.Name)
	}
	// Declared order first, then undeclared leftovers sorted by name.
	if !reflect.DeepEqual(order, []string{"SERVICE_URL", "ADMIN_TOKEN", "NEVER_SET", "AUTO_SECRET", "LEGACY_OUT"}) {
		t.Errorf("order = %v", order)
	}
	if vm := byName["SERVICE_URL"]; !vm.Set || vm.Secret || vm.Value != "https://api.example.com" || vm.Label != "Service URL" {
		t.Errorf("SERVICE_URL = %+v", vm)
	}
	if vm := byName["ADMIN_TOKEN"]; !vm.Set || !vm.Secret || !vm.CanReveal || vm.Value != "tok-123" {
		t.Errorf("ADMIN_TOKEN = %+v", vm)
	}
	if vm := byName["AUTO_SECRET"]; !vm.Secret || !vm.CanReveal {
		t.Errorf("AUTO_SECRET should inherit stored secrecy: %+v", vm)
	}
	if vm := byName["NEVER_SET"]; vm.Set || vm.Value != "" {
		t.Errorf("NEVER_SET = %+v", vm)
	}

	// Without reveal permission, secret values never reach the page.
	for _, vm := range BuildOutputVMs(d, stored, false) {
		if vm.Secret && (vm.Value != "" || vm.CanReveal) {
			t.Errorf("secret value leaked without permission: %+v", vm)
		}
	}
}
