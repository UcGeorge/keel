package cloudserver

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/UcGeorge/keel/internal/mcpauth"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/google/uuid"
)

func TestMCPConnectionPermissions(t *testing.T) {
	f := newMCPFixture(t)
	ctx := context.Background()
	tokens := f.tokens(f.owner, mcpauth.ReadScope)
	p, err := f.s.oauthService().Store.Authenticate(ctx, tokens.AccessToken, f.http.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: f.owner.c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"_csrf": {f.csrf}, "grant_id": {p.GrantID.String()}, "scope": {mcpauth.FullScope}}
	post := func(values url.Values, want int) {
		t.Helper()
		resp, body := f.request("POST", "/account/agents/permissions", "", values.Encode(), "application/x-www-form-urlencoded", client)
		if resp.StatusCode != want {
			t.Fatalf("permissions status %d, want %d: %s", resp.StatusCode, want, body)
		}
	}
	assertScope := func(token, scope string) {
		t.Helper()
		principal, err := f.s.oauthService().Store.Authenticate(ctx, token, f.http.URL+"/mcp")
		if err != nil || principal.Scope != scope {
			t.Fatalf("expected scope %s: principal=%+v err=%v", scope, principal, err)
		}
	}
	for _, tc := range []struct {
		name, key string
		values    []string
		status    int
	}{
		{"missing CSRF", "_csrf", nil, 403},
		{"wrong CSRF", "_csrf", []string{"wrong"}, 403},
		{"missing scope", "scope", nil, 400},
		{"unknown scope", "scope", []string{"admin"}, 400},
		{"combined scopes", "scope", []string{"keel:read keel:full"}, 400},
		{"repeated scope", "scope", []string{mcpauth.ReadScope, mcpauth.FullScope}, 400},
		{"invalid ID", "grant_id", []string{"bad-id"}, 400},
		{"unknown ID", "grant_id", []string{uuid.NewString()}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := cloneForm(form)
			bad[tc.key] = tc.values
			post(bad, tc.status)
			assertScope(tokens.AccessToken, mcpauth.ReadScope)
		})
	}
	// A bearer token cannot invoke the browser-only delegation controls, even
	// when the caller knows the connection ID and supplies a valid CSRF value.
	resp, _ := f.request("POST", "/account/agents/permissions", tokens.AccessToken, form.Encode(), "application/x-www-form-urlencoded", nil)
	if resp.StatusCode != 403 {
		t.Fatalf("bearer-only permission update status %d, want 403", resp.StatusCode)
	}
	assertScope(tokens.AccessToken, mcpauth.ReadScope)
	other, err := f.s.Q.CreateUser(ctx, clouddb.CreateUserParams{Email: uuid.NewString() + "@test.example", Name: "Other owner", PasswordHash: "unusable-test-hash"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.s.Q.CreateMCPGrant(ctx, clouddb.CreateMCPGrantParams{UserID: other.ID, ClientID: f.clientID, Resource: f.http.URL + "/mcp", Scope: mcpauth.ReadScope, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	bad := cloneForm(form)
	bad.Set("grant_id", foreign.ID.String())
	post(bad, 404)
	rows, err := f.s.Q.ListMCPConnections(ctx, other.ID)
	if err != nil || len(rows) != 1 || rows[0].Scope != mcpauth.ReadScope {
		t.Fatal("another user's connection changed")
	}

	// One existing MCP session must observe both upgrades and downgrades without
	// reconnecting or waiting for token expiry. Other grants remain independent.
	independent := f.tokens(f.owner, mcpauth.ReadScope)
	session := f.connect(tokens.AccessToken)
	mutation := map[string]any{"name": "permission-test-" + uuid.NewString()[:8]}
	f.call(session, "create_organization", mutation, 403)
	post(form, 303)
	assertScope(tokens.AccessToken, mcpauth.FullScope)
	assertScope(independent.AccessToken, mcpauth.ReadScope)
	guest := newBrowser(t, f.http)
	guestForm := cloneForm(form)
	guestForm.Set("_csrf", guest.csrf("/login"))
	guestClient := &http.Client{Jar: guest.c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ = f.request("POST", "/account/agents/permissions", tokens.AccessToken, guestForm.Encode(), "application/x-www-form-urlencoded", guestClient)
	if resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/login?") {
		t.Fatal("full-access bearer substituted for the owner's browser session")
	}
	f.call(session, "create_organization", mutation, 303)
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.clientID}, "resource": {f.http.URL + "/mcp"}, "refresh_token": {tokens.RefreshToken}}
	rotated := f.exchange(refresh, 200)
	if rotated.Scope != mcpauth.FullScope {
		t.Fatal("refresh did not report the updated scope")
	}
	form.Set("scope", mcpauth.ReadScope)
	post(form, 303)
	assertScope(tokens.AccessToken, mcpauth.ReadScope)
	assertScope(rotated.AccessToken, mcpauth.ReadScope)
	f.call(session, "create_organization", mutation, 403)
	f.call(f.connect(rotated.AccessToken), "create_organization", mutation, 403)
	f.call(session, "list_organizations", nil, 200)
	refresh.Set("refresh_token", rotated.RefreshToken)
	rotated = f.exchange(refresh, 200)
	if rotated.Scope != mcpauth.ReadScope {
		t.Fatal("refresh restored removed permissions")
	}
	if err := f.s.Q.RevokeMCPGrantForUser(ctx, clouddb.RevokeMCPGrantForUserParams{ID: p.GrantID, UserID: p.UserID}); err != nil {
		t.Fatal(err)
	}
	form.Set("scope", mcpauth.FullScope)
	post(form, 404)
	if _, err := f.s.oauthService().Store.Authenticate(ctx, rotated.AccessToken, f.http.URL+"/mcp"); err == nil {
		t.Fatal("permission change revived a revoked grant")
	}
	expired, err := f.s.Q.CreateMCPGrant(ctx, clouddb.CreateMCPGrantParams{UserID: p.UserID, ClientID: f.clientID, Resource: f.http.URL + "/mcp", Scope: mcpauth.ReadScope, ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	form.Set("grant_id", expired.ID.String())
	post(form, 404)
}
