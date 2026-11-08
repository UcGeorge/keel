package cloudserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/UcGeorge/keel/internal/githubapp"
	"github.com/UcGeorge/keel/internal/mcpauth"
	"github.com/UcGeorge/keel/internal/store/clouddb"
)

func TestOrganizationAuditLog(t *testing.T) {
	f := newMCPFixture(t)
	ctx := t.Context()
	full := f.connect(f.tokens(f.owner, mcpauth.FullScope).AccessToken)
	read := f.connect(f.tokens(f.owner, mcpauth.ReadScope).AccessToken)
	orgName := "Audit " + f.org
	create := func() string {
		return f.call(full, "create_organization", map[string]any{"name": orgName}, 303).Location
	}
	base := create()
	logPath := base + "/audit-log"
	orgSlug := strings.TrimPrefix(base, "/orgs/")
	org, err := f.s.Q.GetOrgBySlug(ctx, orgSlug)
	if err != nil {
		t.Fatal(err)
	}
	orgArgs := map[string]any{"org": orgSlug}
	owner, err := f.s.Q.GetUserByEmail(ctx, f.org+"@test.example")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("audit write failure rolls back the change", func(t *testing.T) {
		event := userAudit(ctx, owner, org, "organization.rename")
		event.Outcome = "rejected_by_the_database"
		err := f.s.auditedChange(ctx, event, func(q *clouddb.Queries) error {
			_, err := q.UpdateOrgName(ctx, clouddb.UpdateOrgNameParams{ID: org.ID, Name: "Rolled back"})
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "record audit event") {
			t.Fatalf("expected the audit constraint to reject the change, got %v", err)
		}
		if fresh, err := f.s.Q.GetOrg(ctx, org.ID); err != nil || fresh.Name != orgName {
			t.Fatalf("organization was changed without an audit entry: %+v, %v", fresh, err)
		}
	})
	f.call(read, "rename_organization", args(orgArgs, "name", "Rejected rename"), 403)

	operator := newBrowser(t, f.http)
	email := "operator-" + f.org + "@test.example"
	const operatorName = "Audit Operator"
	if _, status := operator.post("/signup", url.Values{
		"_csrf": {operator.csrf("/signup")}, "name": {operatorName}, "email": {email}, "password": {"audit-operator-password"},
	}); status != 200 {
		t.Fatalf("signup status = %d", status)
	}
	if _, status := operator.get(logPath); status != http.StatusNotFound {
		t.Fatalf("outsider audit log status = %d", status)
	}
	member := f.connect(f.tokens(operator, mcpauth.FullScope).AccessToken)
	invited := f.call(full, "invite_member", args(orgArgs, "email", email, "role", "member", "can_configure", true), 200)
	link := invited.Data.(map[string]any)["InviteLink"].(string)
	token := link[strings.LastIndex(link, "/")+1:]
	f.call(member, "accept_invite", map[string]any{"token": token}, 303)
	if _, status := operator.get(logPath); status != http.StatusForbidden {
		t.Fatalf("member audit log status = %d", status)
	}
	if body, status := operator.get(base); status != 200 || strings.Contains(body, "href=\""+logPath+"\"") {
		t.Fatal("member navigation exposed the audit log")
	}

	// Seed a repository configuration so this test needs PostgreSQL but
	// doesn't depend on a real Git host or Docker for the audit checks.
	repo, err := f.s.Q.CreateRepo(ctx, clouddb.CreateRepoParams{OrgID: org.ID, Name: "audit-app", Provider: "git", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := strings.Replace(e2eYAML, "label: Greeting", "label: Greeting\n        validation: {min_length: 4}", 1)
	if _, err := f.s.Q.UpdateRepoSync(ctx, clouddb.UpdateRepoSyncParams{ID: repo.ID, Status: "ok", ConfigYaml: cfg}); err != nil {
		t.Fatal(err)
	}
	depArgs := args(orgArgs, "repo", repo.Name, "dep", "prod")
	targetArgs := args(depArgs, "target", "audit-target")
	f.call(member, "create_target", args(depArgs, "name", "audit-target"), 303)
	targetPath := base + "/repos/audit-app/deployments/prod/targets/audit-target"
	const secret = "do-not-log-this-secret"
	const nonSecret = "do-not-log-this-value"
	if _, status := operator.post(targetPath+"/values", url.Values{
		"_csrf": {operator.csrf(targetPath)}, "GREETING": {"x"}, "SECRET_TOKEN": {secret},
	}); status != http.StatusUnprocessableEntity {
		t.Fatalf("partial target value save status = %d", status)
	}
	f.call(member, "save_target_values", args(targetArgs, "values", map[string]string{"GREETING": nonSecret}), 303)

	run, err := f.s.Q.CreateRun(ctx, clouddb.CreateRunParams{
		RepoID: repo.ID, Deployment: "prod", TargetName: "audit-target", TriggerKind: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	const logLine = "do-not-copy-run-logs-to-the-audit-log"
	if err := f.s.Q.AppendRunLog(ctx, clouddb.AppendRunLogParams{RunID: run.ID, Seq: 1, Line: logLine}); err != nil {
		t.Fatal(err)
	}
	if body, status := operator.get(base + "/repos/audit-app/runs/" + run.ID.String() + "/events"); status != 200 || !strings.Contains(body, logLine) {
		t.Fatalf("reading the browser log stream: status %d, body %s", status, body)
	}
	f.call(member, "get_run_logs", args(orgArgs, "repo", repo.Name, "id", run.ID.String()), 200)
	f.call(full, "delete_target", targetArgs, 303)
	f.call(full, "delete_repository", args(orgArgs, "repo", repo.Name, "confirm_name", "wrong-name"), 422)
	f.call(full, "delete_repository", args(orgArgs, "repo", repo.Name, "confirm_name", repo.Name), 303)

	opUser, err := f.s.Q.GetUserByEmail(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	f.call(full, "update_member", args(orgArgs, "user", opUser.ID.String(), "role", "admin"), 303)
	query := "?category=deployment&source=web&outcome=partial&q=" + url.QueryEscape(email)
	body, status := operator.get(logPath + query)
	if status != 200 || !strings.Contains(body, "Partially applied") || !strings.Contains(body, "SECRET_TOKEN (secret)") || strings.Contains(body, "Create target") {
		t.Fatalf("filtered audit log status %d, wanted only the partial value save", status)
	}
	f.call(full, "remove_member", args(orgArgs, "user", opUser.ID.String()), 303)
	if _, status := operator.get(logPath); status != http.StatusNotFound {
		t.Fatalf("removed member audit log status = %d", status)
	}

	rows, err := f.s.Q.ListOrgAuditEvents(ctx, clouddb.ListOrgAuditEventsParams{OrgID: org.ID, BeforeID: math.MaxInt64, PageLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[[3]string]*clouddb.OrgAuditEvent{}
	for _, row := range rows {
		seen[[3]string{row.Action, row.Source, row.Outcome}] = row
	}
	for _, key := range [][3]string{
		{"organization.create", "mcp", auditSucceeded},
		{"organization.rename", "mcp", auditDenied},
		{"organization.audit_view", "web", auditDenied},
		{"organization.audit_view", "web", auditSucceeded},
		{"membership.accept", "mcp", auditSucceeded},
		{"membership.remove", "mcp", auditSucceeded},
		{"deployment.target_create", "mcp", auditSucceeded},
		{"deployment.values_save", "web", auditPartial},
		{"deployment.values_save", "mcp", auditSucceeded},
		{"deployment.target_delete", "mcp", auditSucceeded},
		{"run.logs_view", "web", auditSucceeded},
		{"run.logs_view", "mcp", auditSucceeded},
		{"repository.disconnect", "mcp", auditFailed},
		{"repository.disconnect", "mcp", auditSucceeded},
	} {
		if seen[key] == nil {
			t.Errorf("missing audit event %v", key)
		}
	}
	if saved := seen[[3]string{"deployment.values_save", "web", auditPartial}]; saved == nil ||
		saved.ActorID != opUser.ID.String() || saved.ActorName != operatorName || !saved.ResourceID.Valid ||
		saved.ResourceName != "audit-app / prod / audit-target" || saved.OccurredAt.IsZero() {
		t.Errorf("missing historical actor or target identity: %+v", saved)
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, nonSecret, logLine, token, "wrong-name", "Rejected rename"} {
		if strings.Contains(string(payload), forbidden) {
			t.Errorf("submitted value, log content or credential leaked into the audit trail")
		}
	}

	f.call(full, "delete_organization", args(orgArgs, "confirm_name", orgName), 303)
	deleted, err := f.s.Q.ListOrgAuditEvents(ctx, clouddb.ListOrgAuditEventsParams{OrgID: org.ID, BeforeID: math.MaxInt64, PageLimit: 1})
	if err != nil || len(deleted) != 1 || deleted[0].Action != "organization.delete" || deleted[0].Outcome != auditSucceeded {
		t.Fatalf("deleting the organization lost its audit evidence: %+v, %v", deleted, err)
	}
	if reused := create(); reused != base {
		t.Fatalf("expected to reuse org slug: %q, got %q", base, reused)
	}
	if body, status := f.owner.get(logPath + "?q=" + url.QueryEscape(email)); status != 200 || !strings.Contains(body, "No matching events") || strings.Contains(body, operatorName) {
		t.Fatal("the recreated organization could read the deleted organization's history")
	}
}

func TestOrganizationAuditGitHubPush(t *testing.T) {
	f := newPortalFixture(t)
	ctx := t.Context()
	orgID := mustOrg(t, f.s, f.org)
	git := startGitServer(t, buildTestRepo(t))
	defer git.Close()
	remote := "audit/" + f.org
	// The webhook matches by installation and repository. Clone the
	// fixture's public git URL without calling GitHub's token endpoint.
	repo, err := f.s.Q.CreateRepo(ctx, clouddb.CreateRepoParams{
		OrgID: orgID, Name: "push-audit", Provider: "git", Branch: "main", GitUrl: git.URL + "/repo.git",
		GithubInstallationID: pgInt8(42), GithubFullName: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.s.Q.CreateTarget(ctx, clouddb.CreateTargetParams{
		RepoID: repo.ID, Deployment: "prod", Name: "unconfigured", AutoDeploy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "test-webhook-secret"
	s := &Server{Cfg: f.s.Cfg, Pool: f.s.Pool, Q: f.s.Q, GitHub: &githubapp.App{WebhookSecret: secret}}
	body := fmt.Sprintf(`{"ref":"refs/heads/main","after":"deadbeef","installation":{"id":42},"repository":{"full_name":%q},"sender":{"id":17,"login":"audit-operator"}}`, remote)
	send := func(signature string) int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-Hub-Signature-256", signature)
		w := httptest.NewRecorder()
		s.handleGithubWebhook(w, req)
		return w.Code
	}
	if got := send("sha256=unverified"); got != http.StatusUnauthorized {
		t.Fatalf("unsigned webhook status = %d", got)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	if got := send(fmt.Sprintf("sha256=%x", mac.Sum(nil))); got != http.StatusNoContent {
		t.Fatalf("signed webhook status = %d", got)
	}
	rows, err := s.Q.ListOrgAuditEvents(ctx, clouddb.ListOrgAuditEventsParams{
		OrgID: orgID, BeforeID: math.MaxInt64, Source: "github", PageLimit: 10,
	})
	if err != nil || len(rows) != 2 {
		t.Fatalf("wanted one sync and one auto-deploy attempt, got %d events, err = %v", len(rows), err)
	}
	if sync, deploy := rows[1], rows[0]; sync.Action != "repository.sync" || sync.Outcome != auditSucceeded ||
		sync.ResourceID.UUID != repo.ID || deploy.Action != "run.deploy" || deploy.Outcome != auditFailed ||
		deploy.ResourceID.UUID != target.ID {
		t.Fatalf("unexpected GitHub audit events: %+v, %+v", sync, deploy)
	}
	for _, row := range rows {
		if row.ActorID != "github:17" || row.ActorName != "audit-operator" {
			t.Errorf("incorrect GitHub actor attribution: %+v", row)
		}
	}
}

func TestOrganizationAuditPagination(t *testing.T) {
	f := newPortalFixture(t)
	base := "/orgs/" + f.org
	for range 53 {
		if _, status := f.owner.get(base + "/runs"); status != 200 {
			t.Fatalf("runs status = %d", status)
		}
	}
	idPattern := regexp.MustCompile(`id="audit-(\d+)"`)
	olderPattern := regexp.MustCompile(`href="([^"]+before=[^"]+)"`)
	body, status := f.owner.get(base + "/audit-log?category=run&source=web")
	first := idPattern.FindAllStringSubmatch(body, -1)
	older := olderPattern.FindStringSubmatch(body)
	if status != 200 || len(first) != 50 || len(older) != 2 {
		t.Fatalf("expected a page of 50 events with a cursor; status %d, got %d events", status, len(first))
	}
	if _, status := f.owner.get(base + "/runs"); status != 200 {
		t.Fatalf("runs status = %d", status)
	}
	next := html.UnescapeString(older[1])
	if !strings.Contains(next, "category=run") || !strings.Contains(next, "source=web") {
		t.Fatalf("cursor lost the filters: %s", next)
	}
	body, status = f.owner.get(next)
	second := idPattern.FindAllStringSubmatch(body, -1)
	if status != 200 || len(second) != 3 || olderPattern.MatchString(body) {
		t.Fatalf("expected the final three events; status %d, got %d events", status, len(second))
	}
	for _, row := range second {
		for _, prev := range first {
			if row[1] == prev[1] {
				t.Fatalf("event %s repeated across pages", row[1])
			}
		}
	}
}
