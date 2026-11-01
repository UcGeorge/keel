package cloudserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UcGeorge/keel/internal/auth"
	"github.com/UcGeorge/keel/internal/mcpauth"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport string

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

type mcpFixture struct {
	t                             *testing.T
	s                             *Server
	http                          *httptest.Server
	owner                         *browser
	clientID, redirect, org, csrf string
	seen                          map[string]bool
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("MCP integration requires PostgreSQL")
	}
	if os.Getenv("KEEL_TEST_DATABASE_URL") == "" && !dockerOK() {
		t.Skip("Docker or KEEL_TEST_DATABASE_URL required")
	}
	db, stop := startPostgres(t)
	t.Cleanup(stop)
	h := httptest.NewUnstartedServer(nil)
	s, err := New(context.Background(), Config{DatabaseURL: db, BaseURL: "http://" + h.Listener.Addr().String(), DataDir: t.TempDir()}, "mcp-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h.Config.Handler = s.Handler()
	h.Start()
	t.Cleanup(h.Close)
	f := &mcpFixture{t: t, s: s, http: h, owner: newBrowser(t, h), redirect: "http://127.0.0.1:9876/callback", seen: map[string]bool{}}
	suffix := uuid.NewString()[:8]
	f.org = "mcpowner-" + suffix
	csrf := f.owner.csrf("/signup")
	if body, status := f.owner.post("/signup", url.Values{"_csrf": {csrf}, "name": {"MCP Owner"}, "email": {f.org + "@test.example"}, "password": {"mcp-owner-password1"}}); status != 200 {
		t.Fatalf("signup: %d %s", status, body)
	}
	f.csrf = f.owner.csrf("/account")
	resp, body := f.request("POST", "/oauth/register", "", `{"client_name":"MCP integration agent","redirect_uris":["`+f.redirect+`"],"token_endpoint_auth_method":"none"}`, "application/json", nil)
	if resp.StatusCode != 201 {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	var registered struct {
		ID string `json:"client_id"`
	}
	if json.Unmarshal(body, &registered) != nil || registered.ID == "" {
		t.Fatal("no registered client")
	}
	f.clientID = registered.ID
	return f
}
func (f *mcpFixture) request(method, path, token, body, contentType string, client *http.Client) (*http.Response, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.http.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp, b
}
func (f *mcpFixture) authorize(b *browser, scope string) (code, verifier string, form url.Values) {
	f.t.Helper()
	verifier, _, _ = auth.NewToken()
	hash := sha256.Sum256([]byte(verifier))
	form = url.Values{"client_id": {f.clientID}, "redirect_uri": {f.redirect}, "response_type": {"code"}, "scope": {scope}, "resource": {f.http.URL + "/mcp"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}, "state": {"test-state"}}
	body, status := b.get("/oauth/authorize?" + form.Encode())
	if status != 200 || !strings.Contains(body, "Authorize connection") {
		f.t.Fatalf("consent: %d %s", status, body)
	}
	form.Set("_csrf", b.csrf("/account"))
	form.Set("decision", "allow")
	client := &http.Client{Jar: b.c.Jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, bodyBytes := f.request("POST", "/oauth/authorize", "", form.Encode(), "application/x-www-form-urlencoded", client)
	if resp.StatusCode != 303 {
		f.t.Fatalf("authorize: %d %s", resp.StatusCode, bodyBytes)
	}
	dest, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		f.t.Fatal(err)
	}
	if dest.Query().Get("state") != "test-state" || dest.Query().Get("iss") != f.http.URL {
		f.t.Fatal("lost state/issuer")
	}
	code = dest.Query().Get("code")
	if code == "" {
		f.t.Fatal("missing code")
	}
	return
}
func (f *mcpFixture) exchange(form url.Values, want int) mcpauth.TokenResponse {
	f.t.Helper()
	resp, b := f.request("POST", "/oauth/token", "", form.Encode(), "application/x-www-form-urlencoded", nil)
	if resp.StatusCode != want {
		f.t.Fatalf("token endpoint status %d, want %d: %s", resp.StatusCode, want, b)
	}
	var tokens mcpauth.TokenResponse
	if want == 200 {
		if err := json.Unmarshal(b, &tokens); err != nil {
			f.t.Fatal(err)
		}
		if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.ExpiresIn > 900 {
			f.t.Fatal("invalid tokens")
		}
	}
	return tokens
}
func (f *mcpFixture) tokens(b *browser, scope string) mcpauth.TokenResponse {
	code, v, _ := f.authorize(b, scope)
	return f.exchange(url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID}, "resource": {f.http.URL + "/mcp"}, "redirect_uri": {f.redirect}, "code": {code}, "code_verifier": {v}}, 200)
}
func (f *mcpFixture) connect(token string) *mcp.ClientSession {
	f.t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "keel-integration", Version: "1"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: f.http.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport(token), Timeout: 150 * time.Second}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { sess.Close() })
	return sess
}
func (f *mcpFixture) call(sess *mcp.ClientSession, name string, args map[string]any, want int) mcpResult {
	f.t.Helper()
	f.seen[name] = true
	if args == nil {
		args = map[string]any{}
	}
	result, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		f.t.Fatal(err)
	}
	var out mcpResult
	if json.Unmarshal(b, &out) != nil {
		f.t.Fatalf("bad structured result %s", b)
	}
	if out.Status != want || result.IsError != (want >= 400) {
		f.t.Fatalf("%s status=%d isError=%v, want %d: %s", name, out.Status, result.IsError, want, b)
	}
	if strings.Contains(string(b), `"CSRF"`) {
		f.t.Fatalf("CSRF leaked by %s", name)
	}
	return out
}
func args(base map[string]any, extra ...any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i < len(extra); i += 2 {
		out[extra[i].(string)] = extra[i+1]
	}
	return out
}
func resultText(out mcpResult) string { b, _ := json.Marshal(out); return string(b) }

func TestMCPOAuthAndPortal(t *testing.T) {
	f := newMCPFixture(t)
	full := f.tokens(f.owner, "keel:read keel:full")
	if full.Scope != mcpauth.FullScope {
		t.Fatal("combined scope should grant full")
	}
	owner := f.connect(full.AccessToken)
	listed, err := owner.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != len(mcpOperations()) {
		t.Fatalf("discovered %d tools, want %d", len(listed.Tools), len(mcpOperations()))
	}
	for _, tool := range listed.Tools {
		if tool.InputSchema == nil || tool.Annotations == nil {
			t.Fatalf("missing tool contract: %s", tool.Name)
		}
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server"} {
		resp, _ := f.request("GET", path, "", "", "", nil)
		if resp.StatusCode != 200 {
			t.Fatal("discovery failed")
		}
	}
	rpc := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy","version":"1"}}}`
	resp, b := f.request("POST", "/mcp", full.AccessToken, rpc, "application/json", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "2025-11-25") {
		t.Fatalf("legacy negotiation: %d %s", resp.StatusCode, b)
	}
	for _, bad := range []string{"", full.RefreshToken, "invalid-token"} {
		resp, _ := f.request("POST", "/mcp", bad, rpc, "application/json", nil)
		if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
			t.Fatal("invalid token accepted or missing challenge")
		}
	}
	// A portal cookie is never an MCP credential; an MCP credential is never a portal session.
	resp, _ = f.request("POST", "/mcp", "", rpc, "application/json", f.owner.c)
	if resp.StatusCode != 401 {
		t.Fatal("cookie authenticated MCP")
	}
	resp, _ = f.request("GET", "/account", full.AccessToken, "", "", nil)
	if resp.StatusCode != 303 {
		t.Fatal("MCP token authenticated portal")
	}
	req, _ := http.NewRequest("POST", f.http.URL+"/mcp", strings.NewReader(rpc))
	req.Header.Set("Authorization", "Bearer "+full.AccessToken)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin MCP accepted")
	}
	readTokens := f.tokens(f.owner, mcpauth.ReadScope)
	reader := f.connect(readTokens.AccessToken)
	for _, op := range mcpOperations() {
		if op.Method != "GET" {
			f.call(reader, op.Name, map[string]any{}, 403)
		}
	}
	f.call(reader, "get_account", nil, 200)
	f.call(owner, "list_organizations", nil, 200)
	f.call(owner, "list_agent_connections", nil, 200)
	extra := f.tokens(f.owner, mcpauth.ReadScope)
	extraPrincipal, err := f.s.oauthService().Store.Authenticate(context.Background(), extra.AccessToken, f.http.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	f.call(owner, "revoke_agent_connection", map[string]any{"grant_id": extraPrincipal.GrantID.String()}, 200)
	f.call(owner, "update_profile", map[string]any{"name": "MCP Owner Updated"}, 303)
	orgName := "MCP team " + uuid.NewString()[:8]
	created := f.call(owner, "create_organization", map[string]any{"name": orgName}, 303)
	org := strings.TrimPrefix(created.Location, "/orgs/")
	o := map[string]any{"org": org}
	f.call(owner, "get_organization_settings", o, 200)
	f.call(owner, "rename_organization", args(o, "name", orgName+" renamed"), 303)
	orgName += " renamed"
	f.call(owner, "list_repositories", o, 200)
	f.call(owner, "get_repository_choices", o, 200)
	gitSrv := startGitServer(t, buildTestRepo(t))
	defer gitSrv.Close()
	f.call(owner, "connect_repository", args(o, "provider", "git", "name", "app", "branch", "main", "git_url", gitSrv.URL+"/repo.git"), 303)
	r := args(o, "repo", "app")
	f.call(owner, "get_repository", r, 200)
	f.call(owner, "get_repository_settings", r, 200)
	f.call(owner, "update_repository", args(r, "name", "app", "branch", "main", "git_url", gitSrv.URL+"/repo.git"), 303)
	f.call(owner, "sync_repository", r, 303)
	d := args(r, "dep", "prod")
	f.call(owner, "get_deployment", d, 200)
	f.call(owner, "create_target", args(d, "name", "west", "description", "Integration"), 303)
	target := args(d, "target", "west")
	f.call(owner, "get_target", target, 200)
	f.call(owner, "save_target_values", args(target, "values", map[string]string{"GREETING": "hello-mcp", "SECRET_TOKEN": "secret-mcp-71942"}), 303)
	f.call(owner, "save_target_values", args(target, "values", map[string]string{"TYPO": "unexpected"}), 400)
	state := f.call(owner, "get_target", target, 200)
	if strings.Contains(resultText(state), "secret-mcp-71942") || !strings.Contains(resultText(state), "hello-mcp") {
		t.Fatal("saved secret exposure or lost public value")
	}
	for _, name := range []string{"get_deployment_manifest", "get_target_manifest"} {
		scope := d
		if name == "get_target_manifest" {
			scope = target
		}
		out := f.call(owner, name, scope, 200)
		if !strings.Contains(resultText(out), "Markdown") {
			t.Fatal("manifest missing")
		}
	}
	f.call(owner, "update_target", args(target, "name", "west", "description", "Updated"), 303)
	if dockerOK() {
		started := f.call(owner, "deploy_target", target, 303)
		id := started.Location[strings.LastIndex(started.Location, "/")+1:]
		runArgs := args(r, "id", id)
		deadline := time.Now().Add(180 * time.Second)
		for {
			run, err := f.s.Q.GetRun(context.Background(), parseUUID(id))
			if err != nil {
				t.Fatal(err)
			}
			if run.Status == "succeeded" {
				break
			}
			if run.Status == "failed" || time.Now().After(deadline) {
				t.Fatalf("run did not succeed: %s %s", run.Status, run.Error)
			}
			time.Sleep(200 * time.Millisecond)
		}
		out := f.call(owner, "get_run", runArgs, 200)
		if !strings.Contains(resultText(out), "rk-west-9911") || strings.Contains(resultText(out), "secret-mcp-71942") {
			t.Fatal("wrong output visibility")
		}
		logs := f.call(owner, "get_run_logs", args(runArgs, "limit", 2), 200)
		data := logs.Data.(map[string]any)
		if data["has_more"] != true {
			t.Fatal("log page missing continuation")
		}
		next := int(data["next_after_seq"].(float64))
		f.call(owner, "get_run_logs", args(runArgs, "after_seq", next, "limit", 100), 200)
		f.call(owner, "cancel_run", runArgs, 303)
	} else {
		t.Log("Docker unavailable: live deployment subflow skipped")
	}
	f.call(owner, "list_org_runs", args(o, "limit", 1), 200)
	f.call(owner, "list_repo_runs", args(r, "offset", 0, "limit", 1), 200)
	smtp := startFakeSMTP(t)
	defer smtp.ln.Close()
	f.call(owner, "configure_smtp", args(o, "host", "127.0.0.1", "port", smtp.port(), "encryption", "none", "from_address", "keel@test.example", "from_name", "Keel"), 303)
	f.call(owner, "test_smtp", o, 303)
	f.call(owner, "create_recipient", args(o, "email", "ops@test.example", "events", []string{"run.failed"}, "enabled", true), 303)
	settings := f.call(owner, "get_notifications", o, 200)
	recipients := settings.Data.(map[string]any)["Recipients"].([]any)
	recipient := recipients[0].(map[string]any)["ID"].(string)
	f.call(owner, "update_recipient", args(o, "id", recipient, "email", "ops@test.example", "events", []string{"run.failed"}, "enabled", false), 303)
	f.call(owner, "delete_recipient", args(o, "id", recipient), 303)
	llm := fakeLLM(t)
	defer llm.Close()
	ai := args(o, "base_url", llm.URL+"/v1", "api_key", "e2e-key", "model", "e2e-model")
	f.call(owner, "list_ai_models", ai, 200)
	f.call(owner, "test_ai", ai, 200)
	f.call(owner, "configure_ai", ai, 303)
	f.call(owner, "get_ai_settings", o, 200)
	// Build a failed run without executing Docker so insight checks also work with a supplied PostgreSQL.
	repo, err := f.s.Q.GetRepoByName(context.Background(), clouddb.GetRepoByNameParams{OrgID: mustOrg(t, f.s, org), Lower: "app"})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := f.s.Q.CreateRun(context.Background(), clouddb.CreateRunParams{RepoID: repo.ID, Deployment: "fail", TargetName: "west", TriggerKind: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Q.FinishRun(context.Background(), clouddb.FinishRunParams{ID: failed.ID, Status: "failed", Error: "demo failure", ExitCode: pgInt4(3, true), FailedStep: pgInt4(1, true)}); err != nil {
		t.Fatal(err)
	}
	f.call(owner, "generate_run_insight", args(r, "id", failed.ID.String()), 200)
	f.call(owner, "delete_ai_settings", o, 303)

	viewer := newBrowser(t, f.http)
	viewerEmail := "viewer-" + uuid.NewString()[:8] + "@test.example"
	if _, status := viewer.post("/signup", url.Values{"_csrf": {viewer.csrf("/signup")}, "name": {"Viewer"}, "email": {viewerEmail}, "password": {"viewer-password-1"}}); status != 200 {
		t.Fatal("viewer signup failed")
	}
	viewerTokens := f.tokens(viewer, mcpauth.FullScope)
	member := f.connect(viewerTokens.AccessToken)
	f.call(member, "get_repository", r, 404)
	invite := f.call(owner, "invite_member", args(o, "email", viewerEmail, "role", "member"), 200)
	link := invite.Data.(map[string]any)["InviteLink"].(string)
	token := link[strings.LastIndex(link, "/")+1:]
	f.call(member, "get_invite", map[string]any{"token": token}, 200)
	f.call(member, "accept_invite", map[string]any{"token": token}, 303)
	f.call(member, "get_repository", r, 200)
	if out := f.call(member, "get_target", target, 200); strings.Contains(resultText(out), "rk-west-9911") {
		t.Fatal("viewer received secret output")
	}
	for _, name := range []string{"create_target", "save_target_values", "deploy_target", "update_target", "delete_target", "sync_repository", "delete_repository", "get_ai_settings", "get_notifications", "invite_member", "delete_organization"} {
		switch name {
		case "create_target":
			f.call(member, name, args(d, "name", "denied"), 403)
		case "update_target":
			f.call(member, name, args(target, "name", "denied"), 403)
		case "delete_repository":
			f.call(member, name, args(r, "confirm_name", "app"), 403)
		case "delete_organization":
			f.call(member, name, args(o, "confirm_name", orgName), 403)
		case "invite_member":
			f.call(member, name, args(o, "email", "no@test.example", "role", "member"), 403)
		case "get_ai_settings", "get_notifications":
			f.call(member, name, o, 403)
		case "sync_repository":
			f.call(member, name, r, 403)
		default:
			f.call(member, name, target, 403)
		}
	}
	// Role updates take effect on already connected sessions.
	vuser, err := f.s.Q.GetUserByEmail(context.Background(), viewerEmail)
	if err != nil {
		t.Fatal(err)
	}
	f.call(owner, "list_members", o, 200)
	f.call(owner, "update_member", args(o, "user", vuser.ID.String(), "role", "member", "can_configure", true), 303)
	f.call(member, "create_target", args(d, "name", "allowed-now"), 303)
	f.call(owner, "remove_member", args(o, "user", vuser.ID.String()), 303)
	f.call(member, "get_repository", r, 404)
	f.call(owner, "invite_member", args(o, "email", "pending@test.example", "role", "member"), 200)
	members := f.call(owner, "list_members", o, 200)
	invs := members.Data.(map[string]any)["Invites"].([]any)
	f.call(owner, "revoke_invite", args(o, "invite", invs[0].(map[string]any)["ID"].(string)), 303)
	// Wrong confirmation must be an MCP error, not a misleading successful redirect.
	f.call(owner, "delete_repository", args(r, "confirm_name", "wrong"), 422)
	f.call(owner, "delete_target", target, 303)
	f.call(owner, "delete_repository", args(r, "confirm_name", "app"), 303)
	f.call(owner, "delete_organization", args(o, "confirm_name", orgName), 303)
	f.call(owner, "change_password", map[string]any{"current_password": "mcp-owner-password1", "new_password": "mcp-owner-password2"}, 303)
	resp, _ = f.request("POST", "/mcp", full.AccessToken, rpc, "application/json", nil)
	if resp.StatusCode != 401 {
		t.Fatal("password change did not revoke OAuth")
	}
	for _, op := range mcpOperations() {
		if !f.seen[op.Name] {
			t.Errorf("tool not exercised: %s", op.Name)
		}
	}
}

func mustOrg(t *testing.T, s *Server, slug string) uuid.UUID {
	t.Helper()
	org, err := s.Q.GetOrgBySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func TestMCPOAuthSecurity(t *testing.T) {
	f := newMCPFixture(t)
	code, verifier, consent := f.authorize(f.owner, mcpauth.FullScope)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID}, "resource": {f.http.URL + "/mcp"}, "redirect_uri": {f.redirect}, "code": {code}, "code_verifier": {verifier}}
	for _, pair := range [][2]string{{"code_verifier", strings.Repeat("x", 43)}, {"client_id", "another-client"}, {"resource", "https://other.example/mcp"}, {"redirect_uri", "http://127.0.0.1:9876/other"}} {
		bad := cloneForm(form)
		bad.Set(pair[0], pair[1])
		f.exchange(bad, 400)
	}
	tokens := f.exchange(form, 200)
	// Replaying a correctly bound code revokes the already issued family.
	f.exchange(form, 400)
	if _, err := f.s.oauthService().Store.Authenticate(context.Background(), tokens.AccessToken, f.http.URL+"/mcp"); err == nil {
		t.Fatal("code replay did not revoke family")
	}
	browserClient := &http.Client{Jar: f.owner.c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	badConsent := cloneForm(consent)
	badConsent.Del("_csrf")
	resp, _ := f.request("POST", "/oauth/authorize", "", badConsent.Encode(), "application/x-www-form-urlencoded", browserClient)
	if resp.StatusCode != 403 {
		t.Fatal("CSRF-less consent accepted")
	}
	badConsent = cloneForm(consent)
	badConsent.Set("redirect_uri", "https://attacker.example/callback")
	resp, _ = f.request("GET", "/oauth/authorize?"+badConsent.Encode(), "", "", "", browserClient)
	if resp.StatusCode != 400 || resp.Header.Get("Location") != "" {
		t.Fatal("unregistered redirect accepted")
	}
	badConsent = cloneForm(consent)
	badConsent.Set("decision", "deny")
	resp, _ = f.request("POST", "/oauth/authorize", "", badConsent.Encode(), "application/x-www-form-urlencoded", browserClient)
	if resp.StatusCode != 303 || !strings.Contains(resp.Header.Get("Location"), "error=access_denied") {
		t.Fatal("consent denial failed")
	}
	for _, redirect := range []string{"http://evil.example/cb", "https://u@agent.example/cb", "https://agent.example/cb#fragment", "javascript:alert(1)"} {
		body, _ := json.Marshal(map[string]any{"redirect_uris": []string{redirect}})
		resp, _ := f.request("POST", "/oauth/register", "", string(body), "application/json", nil)
		if resp.StatusCode != 400 {
			t.Fatalf("bad redirect accepted: %s", redirect)
		}
	}
	fresh := f.tokens(f.owner, mcpauth.FullScope)
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.clientID}, "resource": {f.http.URL + "/mcp"}, "refresh_token": {fresh.RefreshToken}}
	rotated := f.exchange(refresh, 200)
	if rotated.RefreshToken == fresh.RefreshToken {
		t.Fatal("refresh token did not rotate")
	}
	f.exchange(refresh, 400)
	if _, err := f.s.oauthService().Store.Authenticate(context.Background(), rotated.AccessToken, f.http.URL+"/mcp"); err == nil {
		t.Fatal("refresh replay did not revoke new access token")
	}
	// Parallel refreshes have exactly one winner, and replay invalidates its family.
	concurrent := f.tokens(f.owner, mcpauth.FullScope)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.s.oauthService().Store.Exchange(context.Background(), "refresh_token", concurrent.RefreshToken, f.clientID, f.http.URL+"/mcp", "", "", "")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent refresh winners: %d", wins)
	}
	revocable := f.tokens(f.owner, mcpauth.FullScope)
	resp, _ = f.request("POST", "/oauth/revoke", "", url.Values{"client_id": {f.clientID}, "token": {revocable.RefreshToken}}.Encode(), "application/x-www-form-urlencoded", nil)
	if resp.StatusCode != 200 {
		t.Fatal("revocation failed")
	}
	if _, err := f.s.oauthService().Store.Authenticate(context.Background(), revocable.AccessToken, f.http.URL+"/mcp"); err == nil {
		t.Fatal("revoked token accepted")
	}
	// Credentials persist beyond handler construction, and account revocation is scoped to the owner.
	persist := f.tokens(f.owner, mcpauth.ReadScope)
	p, err := f.s.oauthService().Store.Authenticate(context.Background(), persist.AccessToken, f.http.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	next := httptest.NewServer(f.s.Handler())
	defer next.Close()
	request, _ := http.NewRequest("POST", next.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Host = strings.TrimPrefix(f.http.URL, "http://")
	request.Header.Set("Authorization", "Bearer "+persist.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("token did not persist across handler reconstruction")
	}
	if body, status := f.owner.get("/account/agents"); status != 200 || !strings.Contains(body, "MCP integration agent") {
		t.Fatal("connection management missing")
	}
	if _, status := f.owner.post("/account/agents/revoke", url.Values{"_csrf": {f.csrf}, "grant_id": {p.GrantID.String()}}); status != 200 {
		t.Fatal("account revocation failed")
	}
	if _, err := f.s.oauthService().Store.Authenticate(context.Background(), persist.AccessToken, f.http.URL+"/mcp"); err == nil {
		t.Fatal("account revocation not effective")
	}
	// Expiration and audience checks use persisted rows, independently of HTTP parsing.
	ctx := context.Background()
	activeGrant, err := f.s.Q.CreateMCPGrant(ctx, clouddb.CreateMCPGrantParams{UserID: p.UserID, ClientID: f.clientID, Resource: f.http.URL + "/mcp", Scope: mcpauth.FullScope, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	expiredToken, expiredHash, _ := auth.NewToken()
	if err = f.s.Q.CreateMCPToken(ctx, clouddb.CreateMCPTokenParams{Hash: expiredHash, GrantID: activeGrant.ID, Kind: "access", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.oauthService().Store.Authenticate(ctx, expiredToken, f.http.URL+"/mcp"); err == nil {
		t.Fatal("expired access token accepted")
	}
	expiredCode, codeHash, _ := auth.NewToken()
	challenge := sha256.Sum256([]byte(verifier))
	if err = f.s.Q.CreateMCPCode(ctx, clouddb.CreateMCPCodeParams{Hash: codeHash, GrantID: activeGrant.ID, RedirectUri: f.redirect, Challenge: base64.RawURLEncoding.EncodeToString(challenge[:]), ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	oldCodeForm := cloneForm(form)
	oldCodeForm.Set("code", expiredCode)
	f.exchange(oldCodeForm, 400)
	expiredGrant, err := f.s.Q.CreateMCPGrant(ctx, clouddb.CreateMCPGrantParams{UserID: p.UserID, ClientID: f.clientID, Resource: f.http.URL + "/mcp", Scope: mcpauth.FullScope, ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	grantToken, grantHash, _ := auth.NewToken()
	if err = f.s.Q.CreateMCPToken(ctx, clouddb.CreateMCPTokenParams{Hash: grantHash, GrantID: expiredGrant.ID, Kind: "access", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.oauthService().Store.Authenticate(ctx, grantToken, f.http.URL+"/mcp"); err == nil {
		t.Fatal("expired grant accepted")
	}
	// A separate server instance reads the same persisted credentials.
	survivor := f.tokens(f.owner, mcpauth.ReadScope)
	restarted, err := New(ctx, f.s.Cfg, "restart-test")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err = restarted.oauthService().Store.Authenticate(ctx, survivor.AccessToken, f.http.URL+"/mcp"); err != nil {
		t.Fatal("token lost across server restart", err)
	}
	if _, err = restarted.oauthService().Store.Authenticate(ctx, survivor.AccessToken, "https://other.example/mcp"); err == nil {
		t.Fatal("token accepted for another audience")
	}
	// Cross-user revocation cannot revoke the other user's connection.
	other, err := f.s.Q.CreateUser(ctx, clouddb.CreateUserParams{Email: "other-" + uuid.NewString() + "@test.example", Name: "Other", PasswordHash: "unusable-test-hash"})
	if err != nil {
		t.Fatal(err)
	}
	otherGrant, err := f.s.Q.CreateMCPGrant(ctx, clouddb.CreateMCPGrantParams{UserID: other.ID, ClientID: f.clientID, Resource: f.http.URL + "/mcp", Scope: mcpauth.ReadScope, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	otherToken, otherHash, _ := auth.NewToken()
	if err = f.s.Q.CreateMCPToken(ctx, clouddb.CreateMCPTokenParams{Hash: otherHash, GrantID: otherGrant.ID, Kind: "access", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, status := f.owner.post("/account/agents/revoke", url.Values{"_csrf": {f.csrf}, "grant_id": {otherGrant.ID.String()}}); status != 200 {
		t.Fatal("revoke request failed")
	}
	if _, err = f.s.oauthService().Store.Authenticate(ctx, otherToken, f.http.URL+"/mcp"); err != nil {
		t.Fatal("another user revoked a connection")
	}
	// HTTP body and host boundaries are enforced before tool execution.
	oversized := strings.Repeat(" ", (1<<20)+1)
	resp, _ = f.request("POST", "/mcp", survivor.AccessToken, oversized, "application/json", nil)
	if resp.StatusCode != 413 {
		t.Fatalf("oversized MCP body: %d", resp.StatusCode)
	}
	hostile, _ := http.NewRequest("POST", f.http.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	hostile.Host = "attacker.example"
	hostile.Header.Set("Authorization", "Bearer "+survivor.AccessToken)
	hostile.Header.Set("Content-Type", "application/json")
	badHost, err := http.DefaultClient.Do(hostile)
	if err != nil {
		t.Fatal(err)
	}
	badHost.Body.Close()
	if badHost.StatusCode != 403 {
		t.Fatal("DNS rebinding host accepted")
	}
	if f.s.oauthService().Store.Allow(context.Background(), "test-limit", 1) != true || f.s.oauthService().Store.Allow(context.Background(), "test-limit", 1) != false {
		t.Fatal("database rate limit ineffective")
	}
}
func cloneForm(in url.Values) url.Values {
	out := url.Values{}
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}
