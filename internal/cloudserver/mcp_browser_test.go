package cloudserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"
)

// HTTP clients do not enforce CSP. Exercise the actual consent form in Chromium
// so a successful HTTP redirect cannot hide a broken browser authorization flow.
// Run with KEEL_TEST_BROWSER=1 after npm ci && npx playwright install chromium.
func TestMCPConsentBrowser(t *testing.T) {
	if os.Getenv("KEEL_TEST_BROWSER") != "1" {
		t.Skip("set KEEL_TEST_BROWSER=1 to run Chromium consent regression")
	}
	f := newMCPFixture(t)
	landing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Agent connected</h1>"))
	}))
	t.Cleanup(landing.Close)
	// A callback may itself redirect to another origin (or hand off to an app).
	// The consent document must allow the entire navigation, not just its first hop.
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, landing.URL+"/complete?"+r.URL.RawQuery, http.StatusSeeOther)
	}))
	t.Cleanup(callback.Close)
	f.redirect = callback.URL + "/callback"
	registration, err := json.Marshal(map[string]any{"client_name": "Browser regression agent", "redirect_uris": []string{f.redirect}})
	if err != nil {
		t.Fatal(err)
	}
	resp, body := f.request("POST", "/oauth/register", "", string(registration), "application/json", nil)
	var registered struct {
		ID string `json:"client_id"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &registered) != nil || registered.ID == "" {
		t.Fatalf("register browser client: %d %s", resp.StatusCode, body)
	}
	f.clientID = registered.ID
	verifier := "browser-test-verifier-012345678901234567890123456789"
	hash := sha256.Sum256([]byte(verifier))
	params := url.Values{"client_id": {f.clientID}, "redirect_uri": {f.redirect}, "response_type": {"code"}, "resource": {f.http.URL + "/mcp"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}
	config, err := json.Marshal(map[string]string{
		"baseURL": f.http.URL, "authorizeURL": f.http.URL + "/oauth/authorize?" + params.Encode(),
		"landingURL": landing.URL + "/complete", "email": f.org + "@test.example", "password": "mcp-owner-password1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/mcp_consent_browser.mjs")
	cmd.Stdin = bytes.NewReader(config)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Chromium consent regression: %v\n%s", err, stderr.String())
	}
	var results []struct {
		Scope string `json:"scope"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil || len(results) != 2 {
		t.Fatalf("invalid browser results: %v: %s", err, stdout.String())
	}
	for _, result := range results {
		tokens := f.exchange(url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID}, "resource": {f.http.URL + "/mcp"}, "redirect_uri": {f.redirect}, "code": {result.Code}, "code_verifier": {verifier}}, http.StatusOK)
		if tokens.Scope != result.Scope {
			t.Fatalf("scope = %q, want %q", tokens.Scope, result.Scope)
		}
		// The browser-issued grant must work with a real MCP client, too.
		session := f.connect(tokens.AccessToken)
		if _, err := session.ListTools(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
}
