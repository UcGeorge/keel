package mcpauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/UcGeorge/keel/internal/auth"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
	"github.com/google/uuid"
)

// User is resolved exclusively from the browser's existing portal session.
type User struct {
	ID         uuid.UUID
	SessionID  uuid.UUID
	Name, CSRF string
}
type Service struct {
	Renderer *web.Renderer
	Base     func(http.ResponseWriter, *http.Request, string) web.Base
	Store    Store
	Issuer   string
	User     func(*http.Request) *User
}

func ValidateIssuer(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !secureURL(u) {
		return fmt.Errorf("KEEL_BASE_URL must be an HTTPS origin (HTTP is allowed only for loopback development)")
	}
	return nil
}
func secureURL(u *url.URL) bool {
	return u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())
}
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 2048 && u.Host != "" && u.User == nil && u.Fragment == "" && secureURL(u) && !strings.ContainsAny(raw, "\r\n\\")
}
func (s *Service) Resource() string { return s.Issuer + "/mcp" }

func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func oauthError(w http.ResponseWriter, status int, code, description string) {
	jsonResponse(w, status, map[string]string{"error": code, "error_description": description})
}
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.serverMetadata)
	mux.HandleFunc("POST /oauth/register", s.register)
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /oauth/revoke", s.revoke)
	mux.HandleFunc("GET /account/agents", s.connections)
	mux.HandleFunc("POST /account/agents/revoke", s.revokeConnection)
}
func (s *Service) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{"resource": s.Resource(), "authorization_servers": []string{s.Issuer}, "scopes_supported": []string{ReadScope, FullScope}, "bearer_methods_supported": []string{"header"}, "resource_name": "Keel MCP"})
}
func (s *Service) serverMetadata(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{
		"authorization_response_iss_parameter_supported": true,
		"issuer": s.Issuer, "authorization_endpoint": s.Issuer + "/oauth/authorize", "token_endpoint": s.Issuer + "/oauth/token",
		"registration_endpoint": s.Issuer + "/oauth/register", "revocation_endpoint": s.Issuer + "/oauth/revoke",
		"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none"}, "revocation_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{ReadScope, FullScope},
	})
}
func (s *Service) limited(w http.ResponseWriter, r *http.Request, kind string, limit int) bool {
	// Global cap limits distributed registration abuse and IP-bucket cardinality.
	if !s.Store.Allow(r.Context(), kind+":global", limit*100) {
		w.Header().Set("Retry-After", "60")
		oauthError(w, 429, "temporarily_unavailable", "Try again later.")
		return true
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	sum := sha256.Sum256([]byte(ip))
	if !s.Store.Allow(r.Context(), kind+":"+hex.EncodeToString(sum[:]), limit) {
		w.Header().Set("Retry-After", "60")
		oauthError(w, 429, "temporarily_unavailable", "Try again later.")
		return true
	}
	return false
}
func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		oauthError(w, 400, "invalid_client_metadata", "Use application/json.")
		return
	}
	if s.limited(w, r, "register", 10) {
		return
	}
	var in struct {
		Name      string   `json:"client_name"`
		Redirects []string `json:"redirect_uris"`
		Method    string   `json:"token_endpoint_auth_method"`
		Grants    []string `json:"grant_types"`
		Responses []string `json:"response_types"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		oauthError(w, 400, "invalid_client_metadata", "Invalid JSON.")
		return
	}
	if in.Method != "" && in.Method != "none" {
		oauthError(w, 400, "invalid_client_metadata", "Only public clients (none) are supported.")
		return
	}
	if len(in.Redirects) == 0 || len(in.Redirects) > 10 || len(in.Name) > 120 {
		oauthError(w, 400, "invalid_client_metadata", "Supply 1–10 redirect URIs and a client name of at most 120 characters.")
		return
	}
	for _, v := range in.Redirects {
		if !validRedirect(v) {
			oauthError(w, 400, "invalid_redirect_uri", "Use HTTPS or an HTTP loopback callback, without userinfo or fragments.")
			return
		}
	}
	for _, v := range in.Grants {
		if v != "authorization_code" && v != "refresh_token" {
			oauthError(w, 400, "invalid_client_metadata", "Unsupported grant type.")
			return
		}
	}
	for _, v := range in.Responses {
		if v != "code" {
			oauthError(w, 400, "invalid_client_metadata", "Only code responses are supported.")
			return
		}
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "MCP client"
	}
	id := uuid.NewString()
	err = s.Store.Q.CreateMCPClient(r.Context(), clouddb.CreateMCPClientParams{ID: id, Name: in.Name, RedirectUris: in.Redirects})
	if err != nil {
		oauthError(w, 503, "temporarily_unavailable", "Registration unavailable.")
		return
	}
	jsonResponse(w, 201, map[string]any{"client_id": id, "client_name": in.Name, "redirect_uris": in.Redirects, "token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}})
}

func (s *Service) browserUser(w http.ResponseWriter, r *http.Request) *User {
	u := s.User(r)
	if u == nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	}
	return u
}

// Authorize validates even denial requests before redirecting anywhere. Consent
// uses only validated immutable registration metadata and requires browser CSRF.
func (s *Service) authorize(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request", "Invalid form.")
		return
	}
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		q = r.PostForm
	}
	for _, key := range []string{"client_id", "redirect_uri", "response_type", "resource", "scope", "code_challenge", "code_challenge_method", "state"} {
		if len(q[key]) > 1 {
			oauthError(w, 400, "invalid_request", "Repeated OAuth parameters are not allowed.")
			return
		}
	}
	client, err := s.Store.Q.GetMCPClient(r.Context(), q.Get("client_id"))
	if err != nil || !slices.Contains(client.RedirectUris, q.Get("redirect_uri")) {
		oauthError(w, 400, "invalid_request", "Unknown client or unregistered redirect URI.")
		return
	}
	if q.Get("response_type") != "code" || q.Get("resource") != s.Resource() || q.Get("code_challenge_method") != "S256" {
		oauthError(w, 400, "invalid_request", "Authorization code, the exact MCP resource, and S256 PKCE are required.")
		return
	}
	challenge, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	if err != nil || len(challenge) != 32 || len(q.Get("state")) > 2048 {
		oauthError(w, 400, "invalid_request", "Invalid challenge or state.")
		return
	}
	scope := normalizeScope(q.Get("scope"))
	if scope == "" {
		scope = ReadScope
	}
	q.Set("scope", scope)
	if scope != ReadScope && scope != FullScope {
		oauthError(w, 400, "invalid_scope", "Choose keel:read or keel:full.")
		return
	}
	user := s.browserUser(w, r)
	if user == nil {
		return
	}
	if r.Method == http.MethodGet {
		fields := url.Values{}
		for _, key := range []string{"client_id", "redirect_uri", "response_type", "resource", "scope", "code_challenge", "code_challenge_method", "state"} {
			fields.Set(key, q.Get(key))
		}
		s.browserHeaders(w)
		s.Renderer.Render(w, http.StatusOK, "auth/mcp_consent.html", web.PageMCPConsent{Base: s.Base(w, r, "Connect an agent"), ClientName: client.Name, Fields: fields, Full: scope == FullScope, RedirectURI: q.Get("redirect_uri"), Issuer: s.Issuer})
		return
	}
	if user.CSRF == "" || q.Get("_csrf") != user.CSRF {
		oauthError(w, 403, "access_denied", "Invalid consent CSRF token.")
		return
	}
	dest, _ := url.Parse(q.Get("redirect_uri"))
	out := dest.Query()
	// Never inherit a code/error from a registered redirect query.
	out.Del("code")
	out.Del("error")
	out.Del("error_description")
	out.Set("state", q.Get("state"))
	out.Set("iss", s.Issuer)
	if q.Get("decision") != "allow" {
		out.Set("error", "access_denied")
	} else {
		if s.limited(w, r, "authorize", 30) {
			return
		}
		code, hash, err := auth.NewToken()
		if err != nil {
			oauthError(w, 500, "server_error", "Could not authorize.")
			return
		}
		tx, err := s.Store.Pool.Begin(r.Context())
		if err != nil {
			oauthError(w, 503, "temporarily_unavailable", "Could not authorize.")
			return
		}
		defer tx.Rollback(r.Context())
		queries := s.Store.Q.WithTx(tx)
		// Serialize consent with password changes, then re-check the browser
		// session after acquiring the lock. A signed-out session cannot mint a grant.
		if _, err = queries.LockUserForMCPApproval(r.Context(), user.ID); err == nil {
			_, err = queries.GetSessionForMCPApproval(r.Context(), clouddb.GetSessionForMCPApprovalParams{ID: user.SessionID, UserID: user.ID})
		}
		if err != nil {
			oauthError(w, 403, "access_denied", "Your session has ended. Sign in again.")
			return
		}
		grant, err := queries.CreateMCPGrant(r.Context(), clouddb.CreateMCPGrantParams{UserID: user.ID, ClientID: client.ID, Resource: s.Resource(), Scope: scope, ExpiresAt: time.Now().Add(GrantTTL)})
		if err == nil {
			err = queries.CreateMCPCode(r.Context(), clouddb.CreateMCPCodeParams{Hash: hash, GrantID: grant.ID, RedirectUri: q.Get("redirect_uri"), Challenge: q.Get("code_challenge"), ExpiresAt: time.Now().Add(CodeTTL)})
		}
		if err == nil {
			err = queries.TouchMCPClient(r.Context(), client.ID)
		}

		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			oauthError(w, 503, "temporarily_unavailable", "Could not authorize.")
			return
		}
		out.Set("code", code)
	}
	dest.RawQuery = out.Encode()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, dest.String(), http.StatusSeeOther)
}
func parseTokenForm(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		oauthError(w, 400, "invalid_request", "Use form-encoded parameters.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request", "Invalid form.")
		return false
	}
	for _, vs := range r.PostForm {
		if len(vs) != 1 {
			oauthError(w, 400, "invalid_request", "Duplicate parameters.")
			return false
		}
	}
	return true
}
func (s *Service) token(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "token", 120) || !parseTokenForm(w, r) {
		return
	}
	f := r.PostForm
	if f.Get("client_id") == "" || f.Get("client_secret") != "" || r.Header.Get("Authorization") != "" {
		oauthError(w, 401, "invalid_client", "Supply the public client_id without a secret.")
		return
	}
	kind := f.Get("grant_type")
	tok := f.Get("code")
	if kind == "refresh_token" {
		tok = f.Get("refresh_token")
	} else if kind != "authorization_code" {
		oauthError(w, 400, "unsupported_grant_type", "Use authorization_code or refresh_token.")
		return
	}
	if len(tok) != 43 || f.Get("resource") != s.Resource() {
		oauthError(w, 400, "invalid_grant", "Invalid credential or resource.")
		return
	}
	tokens, err := s.Store.Exchange(r.Context(), kind, tok, f.Get("client_id"), s.Resource(), f.Get("redirect_uri"), f.Get("code_verifier"), normalizeScope(f.Get("scope")))
	if err != nil {
		if !errors.Is(err, ErrInvalidGrant) {
			oauthError(w, 503, "temporarily_unavailable", "Token exchange is temporarily unavailable.")
			return
		}
		oauthError(w, 400, "invalid_grant", "The credential is invalid, expired, already used, or bound to another client.")
		return
	}
	jsonResponse(w, 200, tokens)
}
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "revoke", 120) || !parseTokenForm(w, r) {
		return
	}
	// RFC 7009: unknown credentials are indistinguishable from successful revocation.
	err := s.Store.Q.RevokeMCPTokenFamily(r.Context(), clouddb.RevokeMCPTokenFamilyParams{ClientID: r.PostForm.Get("client_id"), Hash: auth.HashToken(r.PostForm.Get("token"))})
	if err != nil {
		oauthError(w, 503, "temporarily_unavailable", "Revocation unavailable.")
		return
	}
	jsonResponse(w, 200, map[string]any{})
}

func (s *Service) connections(w http.ResponseWriter, r *http.Request) {
	user := s.browserUser(w, r)
	if user == nil {
		return
	}
	rows, err := s.Store.Q.ListMCPConnections(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Could not load connections", http.StatusServiceUnavailable)
		return
	}
	page := web.PageMCPConnections{Base: s.Base(w, r, "Agent connections"), Resource: s.Resource()}
	for _, row := range rows {
		page.Connections = append(page.Connections, web.MCPConnectionVM{ID: row.ID.String(), Name: row.Name, Scope: row.Scope, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt})
	}
	s.browserHeaders(w)
	s.Renderer.Render(w, http.StatusOK, "cloud/mcp_connections.html", page)
}
func (s *Service) revokeConnection(w http.ResponseWriter, r *http.Request) {
	user := s.browserUser(w, r)
	if user == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil || user.CSRF == "" || r.PostForm.Get("_csrf") != user.CSRF {
		http.Error(w, "Invalid form", 403)
		return
	}
	id, err := uuid.Parse(r.PostForm.Get("grant_id"))
	if err != nil {
		http.Error(w, "Invalid connection", 400)
		return
	}
	if err = s.Store.Q.RevokeMCPGrantForUser(r.Context(), clouddb.RevokeMCPGrantForUserParams{ID: id, UserID: user.ID}); err != nil {
		http.Error(w, "Could not revoke connection", 503)
		return
	}
	http.Redirect(w, r, "/account/agents", http.StatusSeeOther)
}

func (s *Service) browserHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; form-action 'self'; base-uri 'self'")
}

// Full access includes read; clients may request either or both advertised scopes.
func normalizeScope(scope string) string {
	read, full := false, false
	for _, s := range strings.Fields(scope) {
		switch s {
		case ReadScope:
			read = true
		case FullScope:
			full = true
		default:
			return "invalid"
		}
	}
	if full {
		return FullScope
	}
	if read {
		return ReadScope
	}
	return ""
}
