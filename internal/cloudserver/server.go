// Package cloudserver is the Keel Cloud web application: organizations,
// members with roles and scopes, connected repositories, deployment targets,
// and Docker-executed runs — server-rendered with the same templates as
// `keel dev`.
package cloudserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UcGeorge/keel/internal/config"
	"github.com/UcGeorge/keel/internal/engine"
	"github.com/UcGeorge/keel/internal/githubapp"
	"github.com/UcGeorge/keel/internal/gitutil"
	"github.com/UcGeorge/keel/internal/mcpauth"
	"github.com/UcGeorge/keel/internal/runhub"
	"github.com/UcGeorge/keel/internal/secretbox"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
)

// Config is the cloud server configuration, read from the environment.
type Config struct {
	DatabaseURL string
	Addr        string
	BaseURL     string
	DataDir     string
	// EncryptionKeyHex is the 64-char hex AES key; when empty a key is
	// created in DataDir.
	EncryptionKeyHex string
}

// ConfigFromEnv reads configuration from KEEL_* environment variables.
func ConfigFromEnv() Config {
	cfg := Config{
		DatabaseURL:      firstEnv("KEEL_DATABASE_URL", "DATABASE_URL"),
		Addr:             envDefault("KEEL_ADDR", ":8080"),
		BaseURL:          envDefault("KEEL_BASE_URL", "http://localhost:8080"),
		DataDir:          envDefault("KEEL_DATA_DIR", "./keel-data"),
		EncryptionKeyHex: os.Getenv("KEEL_ENCRYPTION_KEY"),
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return cfg
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

func envDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Server is the Keel Cloud application.
type Server struct {
	Cfg      Config
	Pool     *pgxpool.Pool
	Q        *clouddb.Queries
	Box      *secretbox.Box
	Hub      *runhub.Hub
	Runner   *engine.Runner
	Renderer *web.Renderer
	GitHub   *githubapp.App // nil when not configured
	Version  string

	secureCookies bool
	stopJanitor   context.CancelFunc

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// New wires the cloud server: database (with migrations), encryption key,
// templates, and the optional GitHub App.
func New(ctx context.Context, cfg Config, version string) (*Server, error) {
	if err := mcpauth.ValidateIssuer(cfg.BaseURL); err != nil {
		return nil, err
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("KEEL_DATABASE_URL (or DATABASE_URL) is required, e.g. postgres://keel:keel@localhost:5432/keel")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := gitutil.CheckGit(); err != nil {
		return nil, err
	}

	pool, err := clouddb.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	var box *secretbox.Box
	if cfg.EncryptionKeyHex != "" {
		box, err = secretbox.NewFromHex(cfg.EncryptionKeyHex)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("KEEL_ENCRYPTION_KEY: %w", err)
		}
	} else {
		box, err = secretbox.LoadOrCreateKeyFile(filepath.Join(cfg.DataDir, "encryption.key"))
		if err != nil {
			pool.Close()
			return nil, err
		}
		slog.Warn("KEEL_ENCRYPTION_KEY not set — using a generated key in the data directory; set it explicitly in production")
	}

	renderer, err := web.NewRenderer()
	if err != nil {
		pool.Close()
		return nil, err
	}

	gh, err := githubapp.FromEnv()
	if err != nil {
		pool.Close()
		return nil, err
	}
	if gh != nil {
		slog.Info("GitHub App integration enabled", "app_id", gh.AppID)
	}

	s := &Server{
		Cfg:           cfg,
		Pool:          pool,
		Q:             clouddb.New(pool),
		Box:           box,
		Hub:           runhub.New(),
		Runner:        &engine.Runner{},
		Renderer:      renderer,
		GitHub:        gh,
		Version:       version,
		secureCookies: strings.HasPrefix(cfg.BaseURL, "https://"),
		cancels:       map[string]context.CancelFunc{},
	}
	if err := s.Q.MarkInterruptedRuns(ctx); err != nil {
		slog.Warn("mark interrupted runs", "err", err)
	}
	janitorCtx, stopJanitor := context.WithCancel(ctx)
	s.stopJanitor = stopJanitor
	go s.janitor(janitorCtx)
	return s, nil
}

// Close releases resources.
func (s *Server) Close() {
	if s.stopJanitor != nil {
		s.stopJanitor()
	}
	s.Pool.Close()
}

// janitor prunes expired sessions periodically.
func (s *Server) janitor(parent context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		if err := s.Q.DeleteExpiredSessions(ctx); err != nil {
			slog.Warn("prune sessions", "err", err)
		}
		if err := (mcpauth.Store{Pool: s.Pool, Q: s.Q}).Prune(ctx); err != nil {
			slog.Warn("prune MCP credentials", "err", err)
		}
		cancel()
	}
}

// Handler builds the full route table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", http.StripPrefix("/static/", web.StaticHandler()))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("POST /webhooks/github", s.handleGithubWebhook)

	// Auth.
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /signup", s.handleSignupPage)
	mux.HandleFunc("POST /signup", s.handleSignup)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /invites/{token}", s.handleInvitePage)
	mux.HandleFunc("POST /invites/{token}/accept", s.handleInviteAccept)

	// Signed-in, non-org-scoped.
	mux.HandleFunc("GET /{$}", s.withUser(s.handleHome))
	mux.HandleFunc("GET /account", s.withUser(s.handleAccount))
	mux.HandleFunc("POST /account/profile", s.withUser(s.handleAccountProfile))
	mux.HandleFunc("POST /account/password", s.withUser(s.handleAccountPassword))
	mux.HandleFunc("GET /orgs/new", s.withUser(s.handleOrgNewPage))
	mux.HandleFunc("POST /orgs/new", s.withUser(s.handleOrgNew))

	s.registerMCP(mux, s.registerOrgRoutes(mux))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.errorPage(w, r, nil, http.StatusNotFound, "Page not found")
	})

	return s.recoverPanics(s.checkCSRF(mux))
}

// registerOrgRoutes returns the audit action for each org route. The MCP
// adapter also uses the mapping for the tools it dispatches directly.
func (s *Server) registerOrgRoutes(mux *http.ServeMux) map[string]string {
	// Org-scoped. Only automatic table refreshes have no audit action.
	actions := map[string]string{}
	org := func(pattern, action string, h func(http.ResponseWriter, *http.Request, *orgCtx)) {
		actions[pattern] = action
		mux.HandleFunc(pattern, s.withOrg(action, h))
	}
	const o = "/orgs/{org}"
	org("GET "+o, "repository.list", s.handleRepos)
	org("GET "+o+"/repos/new", "repository.choices", s.handleRepoNewPage)
	org("POST "+o+"/repos", "repository.connect", s.handleRepoCreate)
	org("GET "+o+"/runs", "run.list", s.handleOrgRuns)
	org("GET "+o+"/runs-fragment", "", s.handleOrgRunsFragment)
	org("GET "+o+"/audit-log", "organization.audit_view", s.handleAuditLog)
	org("GET "+o+"/members", "membership.list", s.handleMembers)
	org("POST "+o+"/members/invite", "membership.invite", s.handleMemberInvite)
	org("POST "+o+"/members/{user}/update", "membership.update", s.handleMemberUpdate)
	org("POST "+o+"/members/{user}/remove", "membership.remove", s.handleMemberRemove)
	org("POST "+o+"/invites/{invite}/revoke", "membership.revoke", s.handleInviteRevoke)
	org("GET "+o+"/settings", "organization.settings_view", s.handleOrgSettings)
	org("POST "+o+"/settings", "organization.rename", s.handleOrgSettingsSave)
	org("POST "+o+"/delete", "organization.delete", s.handleOrgDelete)
	org("GET "+o+"/notifications", "notification.view", s.handleNotifications)
	org("POST "+o+"/notifications/smtp", "notification.smtp_save", s.handleSMTPSave)
	org("POST "+o+"/notifications/smtp/test", "notification.smtp_test", s.handleSMTPTest)
	org("POST "+o+"/notifications/recipients", "notification.recipient_create", s.handleRecipientCreate)
	org("POST "+o+"/notifications/recipients/{id}/update", "notification.recipient_update", s.handleRecipientUpdate)
	org("POST "+o+"/notifications/recipients/{id}/delete", "notification.recipient_delete", s.handleRecipientDelete)
	org("GET "+o+"/ai", "ai.view", s.handleAI)
	org("POST "+o+"/ai", "ai.save", s.handleAISave)
	org("POST "+o+"/ai/models", "ai.models", s.handleAIModels)
	org("POST "+o+"/ai/test", "ai.test", s.handleAITest)
	org("POST "+o+"/ai/delete", "ai.delete", s.handleAIDelete)

	// Repo-scoped.
	repo := func(pattern, action string, h func(http.ResponseWriter, *http.Request, *repoCtx)) {
		actions[pattern] = action
		mux.HandleFunc(pattern, s.withRepo(action, h))
	}
	const rp = o + "/repos/{repo}"
	const run = rp + "/runs/{id}"
	const dep = rp + "/deployments/{dep}"
	const tgt = dep + "/targets/{target}"
	repo("GET "+rp, "repository.view", s.handleRepo)
	repo("POST "+rp+"/sync", "repository.sync", s.handleRepoSync)
	repo("GET "+rp+"/settings", "repository.settings_view", s.handleRepoSettings)
	repo("POST "+rp+"/settings", "repository.update", s.handleRepoSettingsSave)
	repo("POST "+rp+"/delete", "repository.disconnect", s.handleRepoDelete)
	repo("GET "+rp+"/runs", "run.list", s.handleRepoRuns)
	repo("GET "+rp+"/runs-fragment", "", s.handleRepoRunsFragment)
	repo("GET "+run, "run.view", s.handleRun)
	repo("GET "+run+"/events", "run.logs_view", s.handleRunEvents)
	repo("POST "+run+"/cancel", "run.cancel", s.handleRunCancel)
	repo("POST "+run+"/insight", "run.insight", s.handleRunInsight)
	repo("GET "+dep, "deployment.view", s.handleDeployment)
	repo("POST "+dep+"/targets", "deployment.target_create", s.handleTargetCreate)
	repo("GET "+dep+"/manifest", "deployment.manifest_view", s.handleManifest)
	repo("GET "+tgt, "deployment.target_view", s.handleTarget)
	repo("GET "+tgt+"/manifest", "deployment.manifest_view", s.handleManifest)
	repo("GET "+tgt+"/runs-fragment", "", s.handleTargetRunsFragment)
	repo("POST "+tgt+"/values", "deployment.values_save", s.handleValuesSave)
	repo("POST "+tgt+"/deploy", "run.deploy", s.handleDeploy)
	repo("POST "+tgt+"/update", "deployment.target_update", s.handleTargetUpdate)
	repo("POST "+tgt+"/delete", "deployment.target_delete", s.handleTargetDelete)

	return actions
}

// --- middleware --------------------------------------------------------------

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic serving request", "path", r.URL.Path, "panic", rec)
				s.errorPage(w, r, nil, http.StatusInternalServerError, "Something went wrong")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// checkCSRF verifies the per-session CSRF token on mutating requests.
// The GitHub webhook uses HMAC. MCP and OAuth credential endpoints have
// their own authentication; browser consent and revocation still require CSRF.
func (s *Server) checkCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/webhooks/github" || r.URL.Path == "/mcp" || r.URL.Path == "/oauth/register" || r.URL.Path == "/oauth/token" || r.URL.Path == "/oauth/revoke" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/oauth/authorize" || r.URL.Path == "/account/agents/revoke" {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		}
		sess := s.session(r)
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			token = r.PostFormValue("_csrf")
		}
		want := ""
		if sess != nil {
			want = sess.CsrfToken
		} else if c, err := r.Cookie(anonCSRFCookie); err == nil {
			// Pre-session forms (login, signup, invite) use a double-submit
			// cookie token.
			want = c.Value
		}
		if token == "" || want == "" || token != want {
			s.errorPage(w, r, nil, http.StatusForbidden, "The form has expired — go back, refresh, and try again")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sessionInfo bundles the session row with its user.
type sessionInfo struct {
	clouddb.Session
	User *clouddb.User
}

const sessionCookie = "keel_session"

// session resolves the current session, or nil.
func (s *Server) session(r *http.Request) *sessionInfo {
	if sess, ok := r.Context().Value(mcpSessionKey{}).(*sessionInfo); ok {
		return sess
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	sess, err := s.Q.GetSessionByTokenHash(r.Context(), authHash(c.Value))
	if err != nil {
		return nil
	}
	user, err := s.Q.GetUser(r.Context(), sess.UserID)
	if err != nil {
		return nil
	}
	return &sessionInfo{Session: *sess, User: user}
}

// withUser requires a signed-in user.
func (s *Server) withUser(h func(http.ResponseWriter, *http.Request, *sessionInfo)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.session(r)
		if sess == nil {
			next := r.URL.Path
			if r.URL.RawQuery != "" {
				next += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
			return
		}
		h(w, r, sess)
	}
}

// orgCtx is the resolved organization scope of a request.
type orgCtx struct {
	Sess   *sessionInfo
	Org    *clouddb.Org
	Member *clouddb.OrgMember
}

// Role helpers.
func (o *orgCtx) isOwner() bool { return o.Member.Role == "owner" }
func (o *orgCtx) isAdmin() bool { return o.Member.Role == "owner" || o.Member.Role == "admin" }
func (o *orgCtx) canConfigure() bool {
	return o.isAdmin() || o.Member.CanConfigure
}
func (o *orgCtx) canDeploy() bool {
	return o.isAdmin() || o.Member.CanDeploy
}

func (o *orgCtx) urlBase() string { return "/orgs/" + o.Org.Slug }

// withOrg requires a signed-in user who is a member of the org in the path.
func (s *Server) withOrg(action string, h func(http.ResponseWriter, *http.Request, *orgCtx)) http.HandlerFunc {
	return s.withUser(func(w http.ResponseWriter, r *http.Request, sess *sessionInfo) {
		org, err := s.Q.GetOrgBySlug(r.Context(), r.PathValue("org"))
		if err != nil {
			s.errorPage(w, r, sess, http.StatusNotFound, "Organization not found")
			return
		}
		member, err := s.Q.GetOrgMember(r.Context(), clouddb.GetOrgMemberParams{OrgID: org.ID, UserID: sess.UserID})
		if err != nil {
			// Not a member: indistinguishable from non-existence.
			s.errorPage(w, r, sess, http.StatusNotFound, "Organization not found")
			return
		}
		oc := &orgCtx{Sess: sess, Org: org, Member: member}
		if action == "" {
			h(w, r, oc)
			return
		}
		s.observeAudit(w, r, userAudit(r.Context(), sess.User, org, action), func(w http.ResponseWriter, r *http.Request) {
			h(w, r, oc)
		})
	})
}

// requireAdmin renders a 403 page and returns false unless the member is
// an owner or admin.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request, oc *orgCtx, what string) bool {
	if oc.isAdmin() {
		return true
	}
	s.errorPage(w, r, oc.Sess, http.StatusForbidden, "Only owners and admins can manage "+what)
	return false
}

// repoCtx adds the repository to an org scope.
type repoCtx struct {
	*orgCtx
	Repo *clouddb.Repo
}

func (rc *repoCtx) repoURL() string { return rc.urlBase() + "/repos/" + rc.Repo.Name }

// withRepo requires an org member and resolves the repository.
func (s *Server) withRepo(action string, h func(http.ResponseWriter, *http.Request, *repoCtx)) http.HandlerFunc {
	return s.withOrg(action, func(w http.ResponseWriter, r *http.Request, oc *orgCtx) {
		repo, err := s.Q.GetRepoByName(r.Context(), clouddb.GetRepoByNameParams{
			OrgID: oc.Org.ID, Lower: r.PathValue("repo"),
		})
		if err != nil {
			s.errorPage(w, r, oc.Sess, http.StatusNotFound, "Repository not found")
			return
		}
		auditSubject(r.Context(), "Repository", repo.ID, repo.Name)
		h(w, r, &repoCtx{orgCtx: oc, Repo: repo})
	})
}

// --- page scaffolding --------------------------------------------------------

const anonCSRFCookie = "keel_csrf"

// anonCSRF returns (creating if needed) the pre-session CSRF token.
func (s *Server) anonCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(anonCSRFCookie); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	token, _, err := authNewToken()
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: anonCSRFCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode,
	})
	return token
}

// base assembles layout data. oc may be nil (auth pages, home).
func (s *Server) base(w http.ResponseWriter, r *http.Request, sess *sessionInfo, oc *orgCtx, title string) web.Base {
	b := web.Base{
		Title:   title,
		AppName: "Keel Cloud",
		Mode:    "cloud",
		Flash:   web.TakeFlash(w, r),
		Version: s.Version,
	}
	if sess == nil {
		b.CSRF = s.anonCSRF(w, r)
	}
	if sess != nil {
		b.User = &web.UserInfo{Name: sess.User.Name, Email: sess.User.Email}
		b.CSRF = sess.CsrfToken
		orgs, err := s.Q.ListOrgsForUser(r.Context(), sess.UserID)
		if err == nil {
			for _, o := range orgs {
				item := web.OrgNavItem{Name: o.Name, Slug: o.Slug, Personal: o.Personal}
				if oc != nil && oc.Org.ID == o.ID {
					item.Active = true
				}
				b.Orgs = append(b.Orgs, item)
			}
		}
	}
	if sess != nil && oc == nil {
		// Pages outside an org scope (account, new-org) still show the
		// switcher; give it a neutral label.
		b.ContextName = "Organizations"
	}
	if oc != nil {
		b.ContextName = oc.Org.Name
		b.OrgSlug = oc.Org.Slug
		base := oc.urlBase()
		p := web.CleanPath(r.URL.Path)
		nav := []web.NavItem{
			{Label: "Repositories", Href: base},
			{Label: "Runs", Href: base + "/runs"},
			{Label: "Members", Href: base + "/members"},
		}
		if oc.isAdmin() {
			nav = append(nav, web.NavItem{Label: "Notifications", Href: base + "/notifications"})
			nav = append(nav, web.NavItem{Label: "AI", Href: base + "/ai"})
			nav = append(nav, web.NavItem{Label: "Audit log", Href: base + "/audit-log"})
		}
		if oc.isOwner() {
			nav = append(nav, web.NavItem{Label: "Settings", Href: base + "/settings"})
		}
		for i := range nav {
			switch nav[i].Href {
			case base:
				nav[i].Active = p == base || strings.HasPrefix(p, base+"/repos")
			default:
				nav[i].Active = strings.HasPrefix(p, nav[i].Href)
			}
		}
		b.Nav = nav
	}
	return b
}

func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, sess *sessionInfo, code int, msg string) {
	if sess == nil {
		sess = s.session(r)
	}
	home := "/"
	page := web.PageError{Base: s.base(w, r, sess, nil, msg), Code: code, Message: msg, HomeURL: home}
	s.Renderer.Render(w, code, "error.html", page)
}

// setSessionCookie writes the session cookie.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode,
	})
}

// parseUUID parses a path UUID, returning uuid.Nil on failure.
func parseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// repoConfig parses the stored (synced) configuration of a repository.
func repoConfig(repo *clouddb.Repo) *config.Config {
	if repo.Status != "ok" || repo.ConfigYaml == "" {
		return nil
	}
	cfg, err := config.Parse([]byte(repo.ConfigYaml))
	if err != nil {
		return nil
	}
	return cfg
}
