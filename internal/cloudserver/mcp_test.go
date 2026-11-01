package cloudserver

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/UcGeorge/keel/internal/web"
)

// Every portal action needs an explicit MCP operation or a documented exclusion.
// This catches parity regressions when a future feature adds a portal route.
func TestMCPPortalCoverage(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`mux.HandleFunc\("((?:GET|POST) [^"]+)"`).FindAllStringSubmatch(string(source), -1)
	excluded := map[string]string{
		"GET /healthz":          "Operational health check, not a user action",
		"POST /webhooks/github": "GitHub authenticates independently with webhook HMAC",
		"GET /login":            "OAuth uses browser login", "POST /login": "Passwords are never exchanged for MCP tokens",
		"GET /signup": "Account bootstrap is a browser action", "POST /signup": "OAuth requires an existing account",
		"POST /logout":                  "Browser logout; MCP uses OAuth revocation",
		"GET /{$}":                      "Redirect only; list_organizations supplies the data",
		"GET /orgs/new":                 "Empty form; create_organization describes its fields",
		"GET /orgs/{org}/runs-fragment": "Polling duplicate of list_org_runs",
		"GET /orgs/{org}/repos/{repo}/runs-fragment":                                    "Polling duplicate of list_repo_runs",
		"GET /orgs/{org}/repos/{repo}/deployments/{dep}/targets/{target}/runs-fragment": "Already returned by get_target",
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, op := range mcpOperations() {
		if names[op.Name] {
			t.Fatalf("duplicate tool %s", op.Name)
		}
		names[op.Name] = true
		seen[op.Method+" "+op.Path] = true
		if _, err := json.Marshal(op.schema()); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range routes {
		if !seen[m[1]] && excluded[m[1]] == "" {
			t.Errorf("portal route has no MCP coverage: %s", m[1])
		}
	}
}
func TestMCPSchemaValidation(t *testing.T) {
	var op mcpOperation
	for _, v := range mcpOperations() {
		if v.Name == "save_target_values" {
			op = v
		}
	}
	valid := `{"org":"acme","repo":"web","dep":"prod","target":"eu","values":{"SECRET":"a&b=42","FLAG":"true"}}`
	path, form, err := op.decode(json.RawMessage(valid))
	if err != nil {
		t.Fatal(err)
	}
	if path != "/orgs/acme/repos/web/deployments/prod/targets/eu/values" || form.Get("SECRET") != "a&b=42" {
		t.Fatalf("bad request: %s %v", path, form)
	}
	for _, bad := range []string{`null`, `[]`, `{}`, strings.Replace(valid, `"acme"`, `"../admin"`, 1), strings.Replace(valid, `"acme"`, `".."`, 1), strings.Replace(valid, `"acme"`, `null`, 1), strings.Replace(valid, `"a&b=42"`, `42`, 1), strings.Replace(valid, `"SECRET"`, `"bad-name"`, 1), strings.Replace(valid, `"values"`, `"unknown"`, 1)} {
		if _, _, err := op.decode(json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	for _, op := range mcpOperations() {
		if op.Name == "get_run_logs" {
			for _, n := range []string{"-1", "101", "1.5", "null", `"5"`} {
				raw := `{"org":"acme","repo":"web","id":"run","limit":` + n + `}`
				if _, _, err := op.decode(json.RawMessage(raw)); err == nil {
					t.Errorf("accepted limit %s", n)
				}
			}
		}
	}
}
func TestMCPResponseStripsBrowserCredentials(t *testing.T) {
	w := &mcpResponse{header: make(http.Header)}
	w.RenderData(200, web.PageRun{Base: web.Base{CSRF: "browser-secret", User: &web.UserInfo{Email: "not-part-of-run"}}, Insight: &web.InsightCardVM{CSRF: "nested-secret"}, Outputs: []web.OutputVM{{Name: "PUBLIC", Value: "public-value"}, {Name: "PRIVATE", Secret: true}}})
	b, err := json.Marshal(w.data)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"browser-secret", "nested-secret", "CSRF", "not-part-of-run"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("leaked %s", bad)
		}
	}
	if !strings.Contains(string(b), "public-value") {
		t.Fatal("lost visible data")
	}
}
