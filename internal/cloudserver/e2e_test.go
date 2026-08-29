package cloudserver

// End-to-end test of Keel Cloud: real PostgreSQL (via Docker), a real git
// server (git http-backend over CGI), and real Docker deployment runs.
//
// Skipped with -short or when Docker is unavailable. Set
// KEEL_TEST_DATABASE_URL to reuse an existing PostgreSQL instead of a
// disposable container.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/version"
)

const e2eYAML = `version: 1
deployments:
  prod:
    description: E2E deployment.
    environment:
      dockerfile: deploy/Dockerfile
    steps:
      - name: Greet
        run: echo "greeting is $GREETING for $KEEL_TARGET"
      - name: Use secret
        run: echo "token is $SECRET_TOKEN"
      - name: Read repo
        run: cat marker.txt
      - name: Publish outputs
        run: |
          export SERVICE_URL="https://e2e.example.com/$KEEL_TARGET"
          export ROTATED_KEY="rk-$KEEL_TARGET-9911"
    variables:
      GREETING:
        label: Greeting
        description: What to say.
        manifest: {why: Demo., how: Make something up.}
      SECRET_TOKEN:
        secret: true
    outputs:
      SERVICE_URL:
        label: Service URL
      ROTATED_KEY:
        secret: true
  fail:
    description: E2E failing deployment.
    environment:
      dockerfile: deploy/Dockerfile
    steps:
      - name: Prepare
        run: echo preparing
      - name: Break
        run: |
          echo "about to fail with MODE=$MODE"
          exit 3
    variables:
      MODE:
        label: Mode
        type: select
        deploy_time: true
        default: plan
        options: [plan, apply]
`

func dockerOK() bool { return exec.Command("docker", "version").Run() == nil }

// startPostgres launches a disposable PostgreSQL container and returns its
// URL plus a cleanup function.
func startPostgres(t *testing.T) (string, func()) {
	t.Helper()
	if env := os.Getenv("KEEL_TEST_DATABASE_URL"); env != "" {
		return env, func() {}
	}
	name := "keel-e2e-pg"
	_ = exec.Command("docker", "rm", "-f", name).Run()
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_USER=keel", "-e", "POSTGRES_PASSWORD=keel", "-e", "POSTGRES_DB=keel",
		"-p", "55439:5432", "postgres:16-alpine").CombinedOutput()
	if err != nil {
		t.Fatalf("start postgres: %v\n%s", err, out)
	}
	cleanup := func() { _ = exec.Command("docker", "rm", "-f", name).Run() }
	dbURL := "postgres://keel:keel@127.0.0.1:55439/keel?sslmode=disable"
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		err := exec.Command("docker", "exec", name, "pg_isready", "-U", "keel").Run()
		if err == nil {
			time.Sleep(500 * time.Millisecond) // ready flaps right after init
			return dbURL, cleanup
		}
		time.Sleep(500 * time.Millisecond)
	}
	cleanup()
	t.Fatal("postgres did not become ready")
	return "", nil
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@test", "GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// startGitServer serves root's bare repositories over smart HTTP.
func startGitServer(t *testing.T, root string) *httptest.Server {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("git-http-backend not found: %v", err)
	}
	h := &cgi.Handler{
		Path: backend,
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	return httptest.NewServer(h)
}

// buildTestRepo creates a bare repo with the e2e keel.yaml and returns root.
func buildTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(filepath.Join(work, "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(work, "keel.yaml"), e2eYAML)
	writeFile(t, filepath.Join(work, "deploy", "Dockerfile"), "FROM alpine:3.20\nWORKDIR /workspace\n")
	writeFile(t, filepath.Join(work, "marker.txt"), "marker-file-content\n")
	gitRun(t, work, "init", "-b", "main")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, root, "clone", "--bare", work, filepath.Join(root, "repo.git"))
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// browser is a tiny stateful client with a cookie jar and CSRF extraction.
type browser struct {
	t      *testing.T
	c      *http.Client
	server *httptest.Server
}

func newBrowser(t *testing.T, server *httptest.Server) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, c: &http.Client{Jar: jar, Timeout: 60 * time.Second}, server: server}
}

var csrfRe = regexp.MustCompile(`name="csrf-token" content="([^"]*)"`)

func (b *browser) get(path string) (string, int) {
	b.t.Helper()
	resp, err := b.c.Get(b.server.URL + path)
	if err != nil {
		b.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

func (b *browser) csrf(path string) string {
	b.t.Helper()
	body, code := b.get(path)
	if code != 200 {
		b.t.Fatalf("GET %s = %d", path, code)
	}
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		b.t.Fatalf("no csrf token on %s", path)
	}
	return m[1]
}

// post submits a form; it returns the final page (after redirects) and the
// final status code.
func (b *browser) post(path string, form url.Values) (string, int) {
	b.t.Helper()
	resp, err := b.c.PostForm(b.server.URL+path, form)
	if err != nil {
		b.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

func TestCloudEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if !dockerOK() {
		t.Skip("docker unavailable")
	}

	dbURL, stopPG := startPostgres(t)
	defer stopPG()

	gitRoot := buildTestRepo(t)
	gitSrv := startGitServer(t, gitRoot)
	defer gitSrv.Close()
	repoURL := gitSrv.URL + "/repo.git"

	cfg := Config{
		DatabaseURL: dbURL,
		BaseURL:     "http://keel.test",
		DataDir:     t.TempDir(),
	}
	srv, err := New(context.Background(), cfg, version.Version)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	web := httptest.NewServer(srv.Handler())
	defer web.Close()

	owner := newBrowser(t, web)

	// --- signup creates a personal org --------------------------------------
	csrf := owner.csrf("/signup")
	body, code := owner.post("/signup", url.Values{
		"_csrf": {csrf}, "name": {"E2E Owner"}, "email": {"owner@e2e.test"}, "password": {"ownerpassword1"},
	})
	if code != 200 {
		t.Fatalf("signup final code %d", code)
	}
	if !strings.Contains(body, "Repositories") && !strings.Contains(body, "New organization") {
		t.Fatalf("unexpected post-signup page:\n%.400s", body)
	}
	// Resolve the personal org slug from the home redirect.
	resp, err := owner.c.Get(web.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	orgPath := resp.Request.URL.Path // /orgs/<slug>
	resp.Body.Close()
	if !strings.HasPrefix(orgPath, "/orgs/") {
		t.Fatalf("home redirected to %s", orgPath)
	}
	t.Logf("personal org: %s", orgPath)

	// --- connect the repository ---------------------------------------------
	csrf = owner.csrf(orgPath + "/repos/new")
	body, code = owner.post(orgPath+"/repos", url.Values{
		"_csrf": {csrf}, "provider": {"git"}, "git_url": {repoURL}, "branch": {"main"}, "name": {"e2e-repo"},
	})
	if code != 200 {
		t.Fatalf("connect repo code %d:\n%.500s", code, body)
	}
	repoPath := orgPath + "/repos/e2e-repo"
	body, _ = owner.get(repoPath)
	if !strings.Contains(body, "prod") {
		t.Fatalf("repo page missing deployment:\n%.800s", body)
	}
	if strings.Contains(body, "config_invalid") || strings.Contains(body, "is invalid") {
		t.Fatalf("config reported invalid:\n%.800s", body)
	}

	// --- create a target and save values ------------------------------------
	depPath := repoPath + "/deployments/prod"
	csrf = owner.csrf(depPath)
	body, code = owner.post(depPath+"/targets", url.Values{
		"_csrf": {csrf}, "name": {"client-a"}, "description": {"E2E client"},
	})
	if code != 200 {
		t.Fatalf("create target code %d", code)
	}
	targetPath := depPath + "/targets/client-a"
	body, code = owner.post(targetPath+"/values", url.Values{
		"_csrf": {csrf}, "GREETING": {"ahoy-e2e"}, "SECRET_TOKEN": {"supersecret-e2e-42"},
	})
	if code != 200 || !strings.Contains(body, "Variables saved") {
		t.Fatalf("save values code %d:\n%.400s", code, body)
	}

	// --- deploy and watch the run -------------------------------------------
	body, code = owner.post(targetPath+"/deploy", url.Values{"_csrf": {csrf}})
	if code != 200 {
		t.Fatalf("deploy code %d:\n%.500s", code, body)
	}
	runRe := regexp.MustCompile(`/runs/([0-9a-f-]{36})`)
	m := runRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no run link on page:\n%.500s", body)
	}
	runPath := repoPath + "/runs/" + m[1]

	deadline := time.Now().Add(180 * time.Second)
	status := ""
	for time.Now().Before(deadline) {
		body, _ = owner.get(runPath)
		switch {
		case strings.Contains(body, ">succeeded<"):
			status = "succeeded"
		case strings.Contains(body, ">failed<"):
			status = "failed"
		case strings.Contains(body, ">canceled<"):
			status = "canceled"
		}
		if status != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if status != "succeeded" {
		t.Fatalf("run ended as %q:\n%s", status, tail(body, 3000))
	}
	if !strings.Contains(body, "greeting is ahoy-e2e for client-a") {
		t.Errorf("run logs missing greeting output:\n%s", tail(body, 2000))
	}
	if !strings.Contains(body, "marker-file-content") {
		t.Errorf("run logs missing repo file content (clone/mount problem)")
	}
	if strings.Contains(body, "supersecret-e2e-42") {
		t.Errorf("secret value leaked into run page")
	}
	// Outputs: the non-secret value is shown; the secret one is present for
	// the owner (behind the reveal control) along with the mask.
	if !strings.Contains(body, "https://e2e.example.com/client-a") {
		t.Errorf("run page missing output value:\n%s", tail(body, 2000))
	}
	if !strings.Contains(body, "rk-client-a-9911") || !strings.Contains(body, "••••••••") {
		t.Errorf("secret output not revealable for the owner")
	}
	// The target page surfaces the latest outputs.
	body, _ = owner.get(targetPath)
	if !strings.Contains(body, "Latest outputs") || !strings.Contains(body, "https://e2e.example.com/client-a") {
		t.Errorf("target page missing latest outputs")
	}
	// The run page records the inputs it was started with: the value, and
	// for the secret only that it was set.
	body, _ = owner.get(runPath)
	if !strings.Contains(body, "Inputs") || !strings.Contains(body, "ahoy-e2e") || !strings.Contains(body, "SECRET_TOKEN") {
		t.Errorf("run page missing inputs:\n%s", tail(body, 3000))
	}
	if strings.Contains(body, "supersecret-e2e-42") {
		t.Errorf("secret input value leaked into the run page")
	}

	// --- notifications: SMTP, recipients, and a failed-run email ------------
	smtp := startFakeSMTP(t)
	defer smtp.ln.Close()
	notifPath := orgPath + "/notifications"
	csrf = owner.csrf(notifPath)
	body, code = owner.post(notifPath+"/smtp", url.Values{
		"_csrf": {csrf}, "host": {"127.0.0.1"}, "port": {smtp.port()}, "encryption": {"none"},
		"from_address": {"keel@e2e.test"}, "from_name": {"Keel E2E"},
	})
	if code != 200 || !strings.Contains(body, "Mail server saved") {
		t.Fatalf("smtp save code %d:\n%.500s", code, body)
	}
	if body, code = owner.post(notifPath+"/smtp/test", url.Values{"_csrf": {csrf}}); code != 200 || !strings.Contains(body, "Test email sent") {
		t.Fatalf("smtp test code %d:\n%.500s", code, body)
	}
	if m := awaitMail(t, smtp, "configured correctly"); !strings.Contains(m, "To: owner@e2e.test") {
		t.Errorf("test email not addressed to the owner:\n%.400s", m)
	}
	body, code = owner.post(notifPath+"/recipients", url.Values{
		"_csrf": {csrf}, "email": {"ops@e2e.test"}, "events": {"run.failed", "run.succeeded", "target.created"},
		"include_insight": {"true"},
	})
	if code != 200 || !strings.Contains(body, "ops@e2e.test") || !strings.Contains(body, "+ AI insight") {
		t.Fatalf("add recipient code %d:\n%.500s", code, body)
	}
	// A second recipient without the insight option gets the plain email.
	if _, code = owner.post(notifPath+"/recipients", url.Values{
		"_csrf": {csrf}, "email": {"plain@e2e.test"}, "events": {"run.failed"},
	}); code != 200 {
		t.Fatalf("add plain recipient code %d", code)
	}

	// --- AI insights: list models, test, save -------------------------------
	llmSrv := fakeLLM(t)
	aiPath := orgPath + "/ai"
	csrf = owner.csrf(aiPath)
	aiForm := url.Values{"_csrf": {csrf}, "base_url": {llmSrv.URL + "/v1/"}, "api_key": {"e2e-key"}}
	body, code = owner.post(aiPath+"/models", aiForm)
	if code != 200 || !strings.Contains(body, `value="e2e-model"`) || strings.Contains(body, "text-embedding-x") {
		t.Fatalf("models fragment code %d:\n%.600s", code, body)
	}
	aiForm.Set("model", "e2e-model")
	body, code = owner.post(aiPath+"/test", aiForm)
	if code != 200 || !strings.Contains(body, "answered") || !strings.Contains(body, "hx-swap-oob") {
		t.Fatalf("test fragment code %d:\n%.600s", code, body)
	}
	body, code = owner.post(aiPath, aiForm)
	if code != 200 || !strings.Contains(body, "AI insights are on") {
		t.Fatalf("ai save code %d:\n%.600s", code, body)
	}
	// A wrong key must not be saved.
	bad := url.Values{"_csrf": {csrf}, "base_url": {llmSrv.URL + "/v1"}, "api_key": {"wrong"}, "model": {"e2e-model"}}
	if body, code = owner.post(aiPath, bad); code != 422 || !strings.Contains(body, "not saved") {
		t.Errorf("bad key save code %d:\n%.400s", code, body)
	}

	// --- a failing run: chips, insight, email --------------------------------
	failDep := repoPath + "/deployments/fail"
	csrf = owner.csrf(failDep)
	if _, code = owner.post(failDep+"/targets", url.Values{"_csrf": {csrf}, "name": {"client-b"}}); code != 200 {
		t.Fatalf("create fail target code %d", code)
	}
	awaitMail(t, smtp, "client-b")
	failTarget := failDep + "/targets/client-b"
	body, code = owner.post(failTarget+"/deploy", url.Values{"_csrf": {csrf}, "MODE": {"apply"}})
	if code != 200 {
		t.Fatalf("fail deploy code %d:\n%.500s", code, body)
	}
	fm := runRe.FindStringSubmatch(body)
	if fm == nil {
		t.Fatalf("no run link for the failing deploy")
	}
	failRun := repoPath + "/runs/" + fm[1]
	deadline = time.Now().Add(180 * time.Second)
	status = ""
	for time.Now().Before(deadline) {
		body, _ = owner.get(failRun)
		if strings.Contains(body, ">failed<") {
			status = "failed"
			break
		}
		if strings.Contains(body, ">succeeded<") || strings.Contains(body, ">canceled<") {
			t.Fatalf("failing run did not fail:\n%s", tail(body, 2000))
		}
		time.Sleep(2 * time.Second)
	}
	if status != "failed" {
		t.Fatalf("failing run did not finish:\n%s", tail(body, 2000))
	}
	if !strings.Contains(body, "Chosen when the deploy started") || !strings.Contains(body, ">apply<") {
		t.Errorf("failed run page missing the deploy-time input:\n%s", tail(body, 3000))
	}
	if !strings.Contains(body, "AI insight") || (!strings.Contains(body, "Explain this failure") && !strings.Contains(body, "Regenerate")) {
		t.Errorf("failed run page missing the insight card")
	}
	if tb, _ := owner.get(failTarget); !strings.Contains(tb, "MODE=") {
		t.Errorf("target run table missing the MODE chip")
	}
	// Two failure emails: the plain one, and the one with the auto-generated
	// insight for the recipient who asked for it.
	var plainMail, richMail string
	for i := 0; i < 2; i++ {
		m := awaitMail(t, smtp, "about to fail with MODE=apply")
		if strings.Contains(m, "To: ops@e2e.test") {
			richMail = m
		} else {
			plainMail = m
		}
	}
	if plainMail == "" || richMail == "" {
		t.Fatalf("expected one plain and one rich failure email")
	}
	if !strings.Contains(plainMail, "To: plain@e2e.test") || strings.Contains(firstPart(plainMail), "AI insight") {
		t.Errorf("plain failure email wrong:\n%s", firstPart(plainMail))
	}
	failText := firstPart(richMail)
	for _, want := range []string{"fail → client-b failed", "Failed step:", "2 of 2", "MODE = apply", "/runs/" + fm[1], "AI insight", "The Break step exited on purpose with MODE set to apply (seen in the log)"} {
		if !strings.Contains(failText, want) {
			t.Errorf("failure email missing %q:\n%s", want, failText)
		}
	}
	// The auto-generated insight is already on the run page.
	if body, _ = owner.get(failRun); !strings.Contains(body, "generated for the failure email") || !strings.Contains(body, "exited on purpose") {
		t.Errorf("auto insight missing from the run page:\n%s", tail(body, 2500))
	}
	csrf = csrfRe.FindStringSubmatch(body)[1]
	body, code = owner.post(failRun+"/insight", url.Values{"_csrf": {csrf}})
	if code != 200 || !strings.Contains(body, "with MODE set to apply (seen in the log)") || !strings.Contains(body, "asked by E2E Owner") {
		t.Fatalf("insight fragment code %d:\n%.800s", code, body)
	}
	if body, _ = owner.get(failRun); !strings.Contains(body, "The Break step exited on purpose") || !strings.Contains(body, "e2e-model") {
		t.Errorf("stored insight missing from the run page:\n%s", tail(body, 2000))
	}
	if nb, _ := owner.get(notifPath); !strings.Contains(nb, ">sent<") {
		t.Errorf("delivery log missing sent entries")
	}

	// --- manifest download ---------------------------------------------------
	body, code = owner.get(targetPath + "/manifest?sel=1&var=GREETING&download=md")
	if code != 200 || !strings.Contains(body, "Greeting") || !strings.Contains(body, "Make something up.") {
		t.Errorf("manifest download wrong (code %d):\n%.400s", code, body)
	}

	// --- org runs page shows the run -----------------------------------------
	body, _ = owner.get(orgPath + "/runs")
	if !strings.Contains(body, "client-a") {
		t.Errorf("org runs page missing the run")
	}

	// --- invite a member with no scopes; verify permissions ------------------
	csrf = owner.csrf(orgPath + "/members")
	body, code = owner.post(orgPath+"/members/invite", url.Values{
		"_csrf": {csrf}, "email": {"viewer@e2e.test"}, "role": {"member"},
	})
	if code != 200 {
		t.Fatalf("invite code %d", code)
	}
	linkRe := regexp.MustCompile(`http://keel\.test(/invites/[A-Za-z0-9_-]+)`)
	lm := linkRe.FindStringSubmatch(body)
	if lm == nil {
		t.Fatalf("no invite link on page:\n%.600s", body)
	}
	invitePath := lm[1]

	viewer := newBrowser(t, web)
	vcsrf := viewer.csrf("/signup")
	if _, code = viewer.post("/signup", url.Values{
		"_csrf": {vcsrf}, "name": {"E2E Viewer"}, "email": {"viewer@e2e.test"}, "password": {"viewerpassword1"},
		"next": {invitePath},
	}); code != 200 {
		t.Fatalf("viewer signup code %d", code)
	}
	vcsrf = viewer.csrf(invitePath)
	if body, code = viewer.post(invitePath+"/accept", url.Values{"_csrf": {vcsrf}}); code != 200 {
		t.Fatalf("accept invite code %d:\n%.400s", code, body)
	}
	// Viewer can read the target page…
	body, code = viewer.get(targetPath)
	if code != 200 || !strings.Contains(body, "don't have permission to deploy") {
		t.Errorf("viewer target page (code %d) should be read-only:\n%.300s", code, body)
	}
	// …sees non-secret outputs but never receives secret output values…
	if !strings.Contains(body, "https://e2e.example.com/client-a") {
		t.Errorf("viewer target page missing non-secret output")
	}
	if strings.Contains(body, "rk-client-a-9911") {
		t.Errorf("secret output value sent to a viewer without reveal permission")
	}
	if vbody, _ := viewer.get(runPath); strings.Contains(vbody, "rk-client-a-9911") {
		t.Errorf("secret output value on viewer run page")
	}
	// …but cannot deploy…
	vcsrf2 := csrfRe.FindStringSubmatch(body)[1]
	if _, code = viewer.post(targetPath+"/deploy", url.Values{"_csrf": {vcsrf2}}); code != 403 {
		t.Errorf("viewer deploy code %d, want 403", code)
	}
	// …and cannot manage members, notifications, or AI settings.
	if _, code = viewer.post(orgPath+"/members/invite", url.Values{
		"_csrf": {vcsrf2}, "email": {"x@y.test"}, "role": {"member"},
	}); code != 403 {
		t.Errorf("viewer invite code %d, want 403", code)
	}
	if _, code = viewer.get(orgPath + "/notifications"); code != 403 {
		t.Errorf("viewer notifications code %d, want 403", code)
	}
	if _, code = viewer.get(orgPath + "/ai"); code != 403 {
		t.Errorf("viewer ai code %d, want 403", code)
	}
	// A member can still ask for an insight on the failed run.
	if ib, code := viewer.post(failRun+"/insight", url.Values{"_csrf": {vcsrf2}}); code != 200 || !strings.Contains(ib, "exited on purpose") {
		t.Errorf("viewer insight code %d:\n%.400s", code, ib)
	}

	// --- sessions survive password change; others end ------------------------
	acsrf := owner.csrf("/account")
	if body, code = owner.post("/account/password", url.Values{
		"_csrf": {acsrf}, "current_password": {"ownerpassword1"}, "new_password": {"ownerpassword2!"},
	}); code != 200 || !strings.Contains(body, "Password changed") {
		t.Errorf("password change code %d:\n%.300s", code, body)
	}

	// Sanity: interrupted-run marking is exercised at boot; ensure the query
	// remains valid against the live schema.
	if err := srv.Q.MarkInterruptedRuns(context.Background()); err != nil {
		t.Errorf("MarkInterruptedRuns: %v", err)
	}
	var _ = clouddb.Run{}
}

// fakeSMTP accepts any number of connections and collects the messages.
type fakeSMTP struct {
	ln       net.Listener
	messages chan string
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, messages: make(chan string, 32)}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) port() string {
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	return port
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	w := func(s string) { conn.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			w("250-fake")
			w("250 8BITMIME")
		case cmd == "DATA":
			w("354 go")
			var b strings.Builder
			for {
				l, err := rd.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.messages <- b.String()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

// firstPart decodes the first base64 body part of a MIME message.
func firstPart(msg string) string {
	_, rest, ok := strings.Cut(msg, "base64\r\n\r\n")
	if !ok {
		return ""
	}
	end := strings.Index(rest, "\r\n--")
	if end < 0 {
		end = len(rest)
	}
	data, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(rest[:end], "\r\n", ""))
	return string(data)
}

// awaitMail waits for a message whose decoded text contains want.
func awaitMail(t *testing.T, f *fakeSMTP, want string) string {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for {
		select {
		case m := <-f.messages:
			if strings.Contains(firstPart(m), want) {
				return m
			}
		case <-deadline:
			t.Fatalf("no email containing %q arrived", want)
			return ""
		}
	}
}

// fakeLLM is an OpenAI-compatible server that lists one model and answers
// every chat with a canned insight that echoes part of its input.
func fakeLLM(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer e2e-key" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "e2e-model"}, {"id": "text-embedding-x"}}})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer e2e-key" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		user := req.Messages[len(req.Messages)-1].Content
		reply := "OK"
		if strings.Contains(user, "Explain this failure") {
			reply = "## What happened\nThe Break step exited on purpose"
			if strings.Contains(user, `MODE ("Mode"): "apply" [select, chosen at deploy time]`) {
				reply += " with MODE set to apply"
			}
			if strings.Contains(user, "about to fail with MODE=apply") {
				reply += " (seen in the log)"
			}
			reply += ".\n\n## Likely cause\nA deliberate `exit 3`.\n\n## What to do next\n1. Nothing.\n\n## For the engineer\nRemove the exit."
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": reply}}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
