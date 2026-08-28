package config

import (
	"errors"
	"strings"
	"testing"
)

const validYAML = `
version: 1
deployments:
  aws-production:
    description: Deploy to AWS.
    environment:
      dockerfile: deploy/aws.Dockerfile
      context: deploy
    steps:
      - name: Authenticate
        run: aws sts get-caller-identity
      - name: Deploy
        run: ./deploy.sh
    variables:
      AWS_ACCESS_KEY_ID:
        label: AWS Access Key ID
        secret: true
        description: IAM access key.
        validation:
          pattern: "AKIA[0-9A-Z]{16}"
          message: Must look like AKIAXXXXXXXXXXXXXXXX
        manifest:
          why: Authenticates the deployment with AWS.
          how: Create an IAM user with programmatic access.
      AWS_REGION:
        type: select
        default: us-east-1
        options:
          - us-east-1
          - value: eu-west-1
            label: Europe (Ireland)
      REPLICAS:
        type: number
        required: false
        validation:
          min: 1
          max: 10
      NOTIFY_EMAIL:
        type: email
        required: false
      DEBUG:
        type: boolean
        required: false
  gcp-staging:
    environment:
      dockerfile: deploy/gcp.Dockerfile
    steps:
      - name: Deploy
        run: gcloud app deploy
`

func mustParse(t *testing.T, y string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func TestParseValid(t *testing.T) {
	cfg := mustParse(t, validYAML)
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
	if got := cfg.DeploymentNames(); len(got) != 2 || got[0] != "aws-production" || got[1] != "gcp-staging" {
		t.Errorf("deployment order = %v", got)
	}
	d := cfg.Deployment("aws-production")
	if d == nil {
		t.Fatal("aws-production missing")
	}
	if d.Environment.Dockerfile != "deploy/aws.Dockerfile" || d.Environment.Context != "deploy" {
		t.Errorf("environment = %+v", d.Environment)
	}
	if len(d.Steps) != 2 || d.Steps[0].Name != "Authenticate" {
		t.Errorf("steps = %+v", d.Steps)
	}
	if len(d.Variables) != 5 {
		t.Fatalf("variables = %d, want 5", len(d.Variables))
	}
	// Document order preserved.
	if d.Variables[0].Name != "AWS_ACCESS_KEY_ID" || d.Variables[1].Name != "AWS_REGION" {
		t.Errorf("variable order = %s, %s", d.Variables[0].Name, d.Variables[1].Name)
	}
	key := d.Variable("AWS_ACCESS_KEY_ID")
	if !key.Secret || !key.Required || key.Type != VarText {
		t.Errorf("AWS_ACCESS_KEY_ID = %+v", key)
	}
	if !key.Manifest.Include {
		t.Error("manifest block present should default Include to true")
	}
	region := d.Variable("AWS_REGION")
	if region.Type != VarSelect || len(region.Options) != 2 {
		t.Errorf("AWS_REGION = %+v", region)
	}
	if region.Options[1].EffectiveLabel() != "Europe (Ireland)" {
		t.Errorf("option label = %q", region.Options[1].EffectiveLabel())
	}
	reps := d.Variable("REPLICAS")
	if reps.Required || reps.Validation.Min == nil || *reps.Validation.Min != 1 {
		t.Errorf("REPLICAS = %+v", reps)
	}
	// gcp-staging defaults.
	g := cfg.Deployment("gcp-staging")
	if g.Environment.Context != "." {
		t.Errorf("default context = %q", g.Environment.Context)
	}
}

func errPaths(t *testing.T, y string) []string {
	t.Helper()
	_, err := Parse([]byte(y))
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var verrs *ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("expected *ValidationErrors, got %T: %v", err, err)
	}
	paths := make([]string, len(verrs.Errors))
	for i, e := range verrs.Errors {
		paths[i] = e.Path
	}
	return paths
}

func hasPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		path string
	}{
		{"missing version", "deployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n", "version"},
		{"wrong version", "version: 9\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n", "version"},
		{"no deployments", "version: 1\n", "deployments"},
		{"bad deployment name", "version: 1\ndeployments:\n  Bad_Name:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n", "deployments.Bad_Name"},
		{"missing dockerfile", "version: 1\ndeployments:\n  a:\n    steps: [{name: x, run: y}]\n", "deployments.a.environment.dockerfile"},
		{"absolute dockerfile", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: /etc/D}\n    steps: [{name: x, run: y}]\n", "deployments.a.environment.dockerfile"},
		{"escaping context", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D, context: ../up}\n    steps: [{name: x, run: y}]\n", "deployments.a.environment.context"},
		{"no steps", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n", "deployments.a.steps"},
		{"step missing run", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x}]\n", "deployments.a.steps[0].run"},
		{"bad var name", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      lower: {}\n", "deployments.a.variables.lower"},
		{"reserved var name", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      KEEL_X: {}\n", "deployments.a.variables.KEEL_X"},
		{"bad type", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {type: dropdown}\n", "deployments.a.variables.V.type"},
		{"select without options", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {type: select}\n", "deployments.a.variables.V.options"},
		{"secret with default", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {secret: true, default: oops}\n", "deployments.a.variables.V.default"},
		{"bad pattern", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V:\n        validation: {pattern: \"[\"}\n", "deployments.a.variables.V.validation.pattern"},
		{"invalid default", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {type: number, default: abc}\n", "deployments.a.variables.V.default"},
		{"unknown key", "version: 1\nservices: {}\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n", "services"},
		{"flex without row", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {flex: 2}\n", "deployments.a.variables.V.flex"},
		{"negative flex", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {row: 1, flex: -1}\n", "deployments.a.variables.V.flex"},
		{"non-integer group key", "version: 1\ndeployments:\n  a:\n    groups:\n      creds: Credentials\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {group: 1}\n", "deployments.a.groups.creds"},
		{"duplicate group id", "version: 1\ndeployments:\n  a:\n    groups:\n      1: One\n      1: Uno\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {group: 1}\n", "deployments.a.groups.1"},
		{"unused group", "version: 1\ndeployments:\n  a:\n    groups:\n      9: Orphan\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {}\n", "deployments.a.groups.9"},
		{"bad group collapsed", "version: 1\ndeployments:\n  a:\n    groups:\n      1: {label: L, collapsed: maybe}\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {group: 1}\n", "deployments.a.groups.1.collapsed"},
		{"unknown group key", "version: 1\ndeployments:\n  a:\n    groups:\n      1: {label: L, color: red}\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {group: 1}\n", "deployments.a.groups.1.color"},
		{"non-integer group ref", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {group: credentials}\n", "deployments.a.variables.V.group"},
		{"when unknown var", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {when: {var: NOPE, eq: x}}\n", "deployments.a.variables.V.when.var"},
		{"when self-reference", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      V: {when: {var: V, eq: x}}\n", "deployments.a.variables.V.when.var"},
		{"when cycle", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      A: {when: {var: B, eq: x}}\n      B: {when: {var: A, eq: x}}\n", "deployments.a.variables.A.when"},
		{"config depends on deploy-time", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      D: {deploy_time: true}\n      C: {when: {var: D, set: true}}\n", "deployments.a.variables.C.when.var"},
		{"when gt non-numeric", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      N: {type: number}\n      V: {when: {var: N, gt: lots}}\n", "deployments.a.variables.V.when.gt"},
		{"when eq not an option", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      S: {type: select, options: [one, two]}\n      V: {when: {var: S, eq: three}}\n", "deployments.a.variables.V.when.eq"},
		{"when without operator", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      X: {}\n      V: {when: {var: X}}\n", "deployments.a.variables.V.when"},
		{"when two operators", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      X: {}\n      V: {when: {var: X, eq: a, ne: b}}\n", "deployments.a.variables.V.when"},
		{"when empty in", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      X: {}\n      V: {when: {var: X, in: []}}\n", "deployments.a.variables.V.when.in"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := errPaths(t, tc.yaml)
			if !hasPath(paths, tc.path) {
				t.Errorf("want error at %q, got %v", tc.path, paths)
			}
		})
	}
}

func TestParseLayout(t *testing.T) {
	cfg, err := Parse([]byte(`
version: 1
deployments:
  a:
    description: Short summary.
    long_description: |
      # Heading

      Full **markdown** body.
    groups:
      2: {label: Tuning, description: Optional knobs., collapsed: true}
      1: Credentials
    environment: {dockerfile: D}
    steps: [{name: x, run: y}]
    variables:
      TOKEN: {group: 1, row: 1, flex: 2}
      REGION: {group: 1, row: 1}
      NOTE: {group: 2}
      NAME: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Deployment("a")
	if d.Description != "Short summary." {
		t.Errorf("Description = %q", d.Description)
	}
	if !strings.Contains(d.LongDescription, "Full **markdown** body.") {
		t.Errorf("LongDescription = %q", d.LongDescription)
	}
	if len(d.Groups) != 2 {
		t.Fatalf("got %d groups", len(d.Groups))
	}
	g := d.Group(2)
	if g == nil || g.Label != "Tuning" || g.Description != "Optional knobs." || !g.Collapsed {
		t.Errorf("group 2 = %+v", g)
	}
	if g := d.Group(1); g == nil || g.EffectiveLabel() != "Credentials" || g.Collapsed {
		t.Errorf("group 1 = %+v", g)
	}
	if d.Group(3) != nil {
		t.Error("Group(3) should be nil")
	}
	tok := d.Variable("TOKEN")
	if tok.Group == nil || *tok.Group != 1 || tok.Row == nil || *tok.Row != 1 || tok.Flex != 2 {
		t.Errorf("TOKEN layout = group %v row %v flex %v", tok.Group, tok.Row, tok.Flex)
	}
	if reg := d.Variable("REGION"); reg.Flex != 0 || reg.EffectiveFlex() != 1 {
		t.Errorf("REGION flex = %v (effective %v)", reg.Flex, reg.EffectiveFlex())
	}
	if name := d.Variable("NAME"); name.Group != nil || name.Row != nil {
		t.Errorf("NAME should have no layout, got group %v row %v", name.Group, name.Row)
	}
}

func TestParseInvalidYAML(t *testing.T) {
	if _, err := Parse([]byte("version: [unclosed")); err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestCheckValue(t *testing.T) {
	cfg := mustParse(t, validYAML)
	d := cfg.Deployment("aws-production")

	cases := []struct {
		varName string
		value   string
		ok      bool
	}{
		{"AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE", true},
		{"AWS_ACCESS_KEY_ID", "nope", false},
		{"AWS_REGION", "us-east-1", true},
		{"AWS_REGION", "mars-1", false},
		{"REPLICAS", "5", true},
		{"REPLICAS", "0", false},
		{"REPLICAS", "11", false},
		{"REPLICAS", "many", false},
		{"NOTIFY_EMAIL", "ops@example.com", true},
		{"NOTIFY_EMAIL", "not-an-email", false},
		{"DEBUG", "true", true},
		{"DEBUG", "yes", false},
	}
	for _, tc := range cases {
		v := d.Variable(tc.varName)
		msg := CheckValue(v, tc.value)
		if tc.ok && msg != "" {
			t.Errorf("%s=%q: unexpected problem %q", tc.varName, tc.value, msg)
		}
		if !tc.ok && msg == "" {
			t.Errorf("%s=%q: expected a problem", tc.varName, tc.value)
		}
	}
}

func TestCheckValues(t *testing.T) {
	cfg := mustParse(t, validYAML)
	d := cfg.Deployment("aws-production")

	problems := CheckValues(d, map[string]string{})
	// AWS_ACCESS_KEY_ID required with no default; AWS_REGION has a default.
	if len(problems) != 1 || problems[0].Name != "AWS_ACCESS_KEY_ID" {
		t.Errorf("problems = %+v", problems)
	}

	problems = CheckValues(d, map[string]string{
		"AWS_ACCESS_KEY_ID": "AKIAIOSFODNN7EXAMPLE",
		"UNDECLARED":        "x",
	})
	if len(problems) != 1 || problems[0].Name != "UNDECLARED" {
		t.Errorf("problems = %+v", problems)
	}
}

func TestResolveValues(t *testing.T) {
	cfg := mustParse(t, validYAML)
	d := cfg.Deployment("aws-production")
	env := ResolveValues(d, map[string]string{"AWS_ACCESS_KEY_ID": "AKIAIOSFODNN7EXAMPLE"})
	if env["AWS_REGION"] != "us-east-1" {
		t.Errorf("default not applied: %v", env)
	}
	if env["DEBUG"] != "false" {
		t.Errorf("boolean default = %q, want false", env["DEBUG"])
	}
	if _, ok := env["REPLICAS"]; ok {
		t.Error("optional empty variable should be omitted")
	}
}

func TestStarterIsValid(t *testing.T) {
	if _, err := Parse([]byte(StarterYAML)); err != nil {
		t.Fatalf("starter keel.yaml must validate: %v", err)
	}
}

func TestInit(t *testing.T) {
	dir := t.TempDir()
	res, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.CreatedConfig || !res.CreatedDockerfile {
		t.Errorf("res = %+v", res)
	}
	if _, err := LoadDir(dir); err != nil {
		t.Fatalf("LoadDir after Init: %v", err)
	}
	// Second init must not overwrite.
	res2, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res2.CreatedConfig || res2.CreatedDockerfile {
		t.Errorf("second init overwrote files: %+v", res2)
	}
}

func TestFindNotFound(t *testing.T) {
	_, err := Find(t.TempDir())
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("want NotFoundError, got %v", err)
	}
	if !strings.Contains(nf.Error(), "keel.yaml") {
		t.Errorf("message = %q", nf.Error())
	}
}

const conditionalYAML = `
version: 1
deployments:
  a:
    environment: {dockerfile: D}
    steps: [{name: x, run: y}]
    variables:
      ACTION:
        type: select
        deploy_time: true
        default: deploy
        options: [deploy, destroy]
      RUN_MODE:
        type: select
        deploy_time: true
        default: full
        options: [full, plan]
        when: {var: ACTION, eq: deploy}
      INCLUDE_MIGRATIONS:
        type: boolean
        required: false
        deploy_time: true
        when: {var: RUN_MODE, eq: full}
      DESTROY_MODE:
        type: select
        deploy_time: true
        options: [destroy-all, destroy-data]
        when: {var: ACTION, eq: destroy}
      CONFIRM:
        deploy_time: true
        when: {var: ACTION, eq: destroy}
        validation: {pattern: destroy, message: type destroy}
      NAME: {}
      REPLICAS:
        type: number
        required: false
        when: {var: NAME, set: true}
`

// TestConditionFlow locks in the conditional-variable semantics across
// ActiveSet, CheckValues, CheckConfigValues, and ResolveValues, including
// chained conditions (INCLUDE_MIGRATIONS → RUN_MODE → ACTION).
func TestConditionFlow(t *testing.T) {
	cfg, err := Parse([]byte(conditionalYAML))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Deployment("a")

	// Defaults: ACTION=deploy activates the deploy branch, chained down to
	// INCLUDE_MIGRATIONS; the destroy branch and REPLICAS are inactive.
	active := d.ActiveSet(nil)
	for name, want := range map[string]bool{
		"ACTION": true, "RUN_MODE": true, "INCLUDE_MIGRATIONS": true,
		"DESTROY_MODE": false, "CONFIRM": false, "NAME": true, "REPLICAS": false,
	} {
		if active[name] != want {
			t.Errorf("default ActiveSet[%s] = %v, want %v", name, active[name], want)
		}
	}

	// Inactive required variables are not problems; NAME still is.
	if problems := CheckValues(d, nil); len(problems) != 1 || problems[0].Name != "NAME" {
		t.Errorf("CheckValues(defaults) = %v", problems)
	}

	// Flipping to destroy activates that branch: DESTROY_MODE and CONFIRM
	// become required, RUN_MODE stops mattering.
	problems := CheckValues(d, map[string]string{"NAME": "n", "ACTION": "destroy"})
	names := map[string]bool{}
	for _, p := range problems {
		names[p.Name] = true
	}
	if len(problems) != 2 || !names["DESTROY_MODE"] || !names["CONFIRM"] {
		t.Errorf("CheckValues(destroy) = %v", problems)
	}
	problems = CheckValues(d, map[string]string{"NAME": "n", "ACTION": "destroy", "DESTROY_MODE": "destroy-data", "CONFIRM": "nope"})
	if len(problems) != 1 || problems[0].Name != "CONFIRM" || problems[0].Message != "type destroy" {
		t.Errorf("CheckValues(bad confirm) = %v", problems)
	}

	// Readiness ignores deploy-time variables entirely.
	if problems := CheckConfigValues(d, map[string]string{"NAME": "n"}); len(problems) != 0 {
		t.Errorf("CheckConfigValues = %v", problems)
	}

	// Deploy branch resolution: the destroy branch is not exported, the
	// active boolean coerces to false.
	env := ResolveValues(d, map[string]string{"NAME": "n"})
	if env["ACTION"] != "deploy" || env["RUN_MODE"] != "full" || env["INCLUDE_MIGRATIONS"] != "false" {
		t.Errorf("deploy env = %v", env)
	}
	for _, name := range []string{"DESTROY_MODE", "CONFIRM"} {
		if _, ok := env[name]; ok {
			t.Errorf("deploy env leaks inactive %s", name)
		}
	}

	// Destroy branch: RUN_MODE must NOT be exported even though it has a
	// default, and the chained INCLUDE_MIGRATIONS must not coerce to false.
	env = ResolveValues(d, map[string]string{"NAME": "n", "ACTION": "destroy", "DESTROY_MODE": "destroy-data", "CONFIRM": "destroy"})
	if env["ACTION"] != "destroy" || env["DESTROY_MODE"] != "destroy-data" || env["CONFIRM"] != "destroy" {
		t.Errorf("destroy env = %v", env)
	}
	for _, name := range []string{"RUN_MODE", "INCLUDE_MIGRATIONS"} {
		if _, ok := env[name]; ok {
			t.Errorf("destroy env leaks inactive %s (deploy-branch)", name)
		}
	}

	// Deploy-time / config partitioning.
	if got := len(d.DeployTimeVariables()); got != 5 {
		t.Errorf("DeployTimeVariables = %d, want 5", got)
	}
	if got := len(d.ConfigVariables()); got != 2 {
		t.Errorf("ConfigVariables = %d, want 2", got)
	}
}

// TestConditionHolds covers each operator, including numeric comparisons
// and their non-numeric fallbacks.
func TestConditionHolds(t *testing.T) {
	cases := []struct {
		c    Condition
		ref  string
		want bool
	}{
		{Condition{Op: CondEq, Value: "a"}, "a", true},
		{Condition{Op: CondEq, Value: "a"}, "b", false},
		{Condition{Op: CondEq, Value: "a"}, "", false},
		{Condition{Op: CondNe, Value: "a"}, "b", true},
		{Condition{Op: CondNe, Value: "a"}, "", false},
		{Condition{Op: CondIn, Values: []string{"a", "b"}}, "b", true},
		{Condition{Op: CondIn, Values: []string{"a", "b"}}, "c", false},
		{Condition{Op: CondGt, Value: "5"}, "6", true},
		{Condition{Op: CondGt, Value: "5"}, "5", false},
		{Condition{Op: CondGte, Value: "5"}, "5", true},
		{Condition{Op: CondLt, Value: "5"}, "4.5", true},
		{Condition{Op: CondLte, Value: "5"}, "5", true},
		{Condition{Op: CondGt, Value: "5"}, "many", false},
		{Condition{Op: CondSet, Want: true}, "x", true},
		{Condition{Op: CondSet, Want: true}, "", false},
		{Condition{Op: CondSet, Want: false}, "", true},
		{Condition{Op: CondSet, Want: false}, "x", false},
	}
	for _, tc := range cases {
		if got := tc.c.Holds(tc.ref); got != tc.want {
			t.Errorf("(%+v).Holds(%q) = %v, want %v", tc.c, tc.ref, got, tc.want)
		}
	}
}

func TestParseOutputs(t *testing.T) {
	cfg, err := Parse([]byte(`
version: 1
deployments:
  a:
    environment: {dockerfile: D}
    steps: [{name: x, run: y}]
    variables:
      DB_PASSWORD: {secret: true}
    outputs:
      SERVICE_URL:
        label: Service URL
        description: Where the API is reachable.
      DB_PASSWORD: {secret: true}
      RAW: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Deployment("a")
	if got := d.OutputNames(); len(got) != 3 || got[0] != "SERVICE_URL" || got[2] != "RAW" {
		t.Errorf("output order = %v", got)
	}
	u := d.Output("SERVICE_URL")
	if u == nil || u.EffectiveLabel() != "Service URL" || u.Secret {
		t.Errorf("SERVICE_URL = %+v", u)
	}
	if o := d.Output("RAW"); o == nil || o.EffectiveLabel() != "RAW" {
		t.Errorf("RAW = %+v", o)
	}
	if !d.Output("DB_PASSWORD").Secret {
		t.Error("DB_PASSWORD should be secret")
	}
}

func TestOutputValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		path string
	}{
		{"bad output name", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    outputs:\n      lower: {}\n", "deployments.a.outputs.lower"},
		{"reserved output name", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    outputs:\n      KEEL_X: {}\n", "deployments.a.outputs.KEEL_X"},
		{"duplicate output", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    outputs:\n      X: {}\n      X: {}\n", "deployments.a.outputs.X"},
		{"unknown output key", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    outputs:\n      X: {default: nope}\n", "deployments.a.outputs.X.default"},
		{"secret variable needs secret output", "version: 1\ndeployments:\n  a:\n    environment: {dockerfile: D}\n    steps: [{name: x, run: y}]\n    variables:\n      TOKEN: {secret: true}\n    outputs:\n      TOKEN: {}\n", "deployments.a.outputs.TOKEN.secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := errPaths(t, tc.yaml)
			if !hasPath(paths, tc.path) {
				t.Errorf("want error at %q, got %v", tc.path, paths)
			}
		})
	}
}
