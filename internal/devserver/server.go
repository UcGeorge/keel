// Package devserver is the `keel dev` web UI: a server-rendered interface
// over the repository the command was run from. It reads keel.yaml on every
// page load (edit the file, refresh the page), keeps local state in
// .keel/dev.db, and executes deployments with the shared Docker engine.
package devserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/smart-minds/keel/internal/config"
	"github.com/smart-minds/keel/internal/engine"
	"github.com/smart-minds/keel/internal/runhub"
	"github.com/smart-minds/keel/internal/secretbox"
	"github.com/smart-minds/keel/internal/store/devdb"
	"github.com/smart-minds/keel/internal/web"
)

// Server is the keel dev application.
type Server struct {
	RepoDir  string
	Version  string
	DB       *sql.DB
	Q        *devdb.Queries
	Box      *secretbox.Box
	Hub      *runhub.Hub
	Runner   *engine.Runner
	Renderer *web.Renderer

	csrf string

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// New wires up a Server rooted at repoDir. It creates .keel/, opens the
// local database, and loads (or creates) the machine encryption key.
func New(repoDir, version string) (*Server, error) {
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}
	keelDir := filepath.Join(abs, ".keel")
	if err := os.MkdirAll(keelDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", keelDir, err)
	}
	ensureGitignore(keelDir)

	db, err := devdb.Open(filepath.Join(keelDir, "dev.db"))
	if err != nil {
		return nil, err
	}

	cfgDir, err := os.UserConfigDir()
	if err != nil {
		cfgDir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	box, err := secretbox.LoadOrCreateKeyFile(filepath.Join(cfgDir, "keel", "dev.key"))
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("load encryption key: %w", err)
	}

	renderer, err := web.NewRenderer()
	if err != nil {
		db.Close()
		return nil, err
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		db.Close()
		return nil, err
	}

	s := &Server{
		RepoDir:  abs,
		Version:  version,
		DB:       db,
		Q:        devdb.New(db),
		Box:      box,
		Hub:      runhub.New(),
		Runner:   &engine.Runner{},
		Renderer: renderer,
		csrf:     hex.EncodeToString(tokenBytes),
		cancels:  map[string]context.CancelFunc{},
	}

	// Runs left over from a previous process can never complete.
	if err := s.Q.MarkInterruptedRuns(context.Background(), sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}); err != nil {
		slog.Warn("mark interrupted runs", "err", err)
	}
	return s, nil
}

// Close releases server resources.
func (s *Server) Close() { s.DB.Close() }

// keelGitignore keeps .keel's machine-local state out of version control
// while leaving bin/ committable — bin/ holds the CLI binaries that
// `make embed` (in the Keel repository) vendors into a project so its team
// can run Keel without the source tree.
const keelGitignore = `# Managed by Keel. Everything in .keel is machine-local state (dev.db, …),
# except the vendored CLI binaries under bin/, which are meant to be
# committed so the whole team can run ` + "`make keel-*`" + ` without the Keel source.
*
!.gitignore
!bin
!bin/**
`

// ensureGitignore makes sure .keel/ ignores its own contents so local state
// never lands in version control. An existing file is upgraded only from
// the historical plain-"*" content; anything else is left alone.
func ensureGitignore(keelDir string) {
	p := filepath.Join(keelDir, ".gitignore")
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) || (err == nil && strings.TrimSpace(string(data)) == "*") {
		_ = os.WriteFile(p, []byte(keelGitignore), 0o644)
	}
}

// Handler builds the HTTP handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", http.StripPrefix("/static/", web.StaticHandler()))

	mux.HandleFunc("GET /{$}", s.handleDashboard)
	mux.HandleFunc("GET /config", s.handleConfig)

	mux.HandleFunc("GET /deployments/{dep}", s.handleDeployment)
	mux.HandleFunc("POST /deployments/{dep}/targets", s.handleTargetCreate)
	mux.HandleFunc("GET /deployments/{dep}/manifest", s.handleManifest)
	mux.HandleFunc("GET /deployments/{dep}/targets/{target}", s.handleTarget)
	mux.HandleFunc("GET /deployments/{dep}/targets/{target}/manifest", s.handleManifest)
	mux.HandleFunc("POST /deployments/{dep}/targets/{target}/values", s.handleValuesSave)
	mux.HandleFunc("POST /deployments/{dep}/targets/{target}/deploy", s.handleDeploy)
	mux.HandleFunc("POST /deployments/{dep}/targets/{target}/update", s.handleTargetUpdate)
	mux.HandleFunc("POST /deployments/{dep}/targets/{target}/delete", s.handleTargetDelete)
	mux.HandleFunc("GET /deployments/{dep}/targets/{target}/runs-fragment", s.handleRunsFragment)

	mux.HandleFunc("GET /runs", s.handleRuns)
	mux.HandleFunc("GET /runs-fragment", s.handleRunsFragment)
	mux.HandleFunc("GET /runs/{id}", s.handleRun)
	mux.HandleFunc("GET /runs/{id}/events", s.handleRunEvents)
	mux.HandleFunc("POST /runs/{id}/cancel", s.handleRunCancel)

	mux.HandleFunc("/", s.handleNotFound)

	return s.recoverPanics(s.checkCSRF(mux))
}

// --- middleware --------------------------------------------------------------

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic serving request", "path", r.URL.Path, "panic", rec)
				s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) checkCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			token = r.PostFormValue("_csrf")
		}
		if token != s.csrf {
			s.errorPage(w, r, http.StatusForbidden, "The form has expired — go back, refresh, and try again")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- helpers -----------------------------------------------------------------

// base assembles the layout data for a page.
func (s *Server) base(w http.ResponseWriter, r *http.Request, title string) web.Base {
	nav := []web.NavItem{
		{Label: "Deployments", Href: "/"},
		{Label: "Runs", Href: "/runs"},
		{Label: "Configuration", Href: "/config"},
	}
	p := web.CleanPath(r.URL.Path)
	for i := range nav {
		switch nav[i].Href {
		case "/":
			nav[i].Active = p == "/" || strings.HasPrefix(p, "/deployments")
		default:
			nav[i].Active = strings.HasPrefix(p, nav[i].Href)
		}
	}
	return web.Base{
		Title:       title,
		AppName:     "Keel",
		Mode:        "dev",
		Nav:         nav,
		ContextName: s.RepoDir,
		Flash:       web.TakeFlash(w, r),
		CSRF:        s.csrf,
		Version:     s.Version,
	}
}

// loadConfig re-reads keel.yaml, returning the parsed config (possibly nil)
// and its status view.
func (s *Server) loadConfig() (*config.Config, web.ConfigStatusVM, string) {
	path, err := config.Find(s.RepoDir)
	if err != nil {
		return nil, web.ConfigStatusVM{Missing: true, Source: s.RepoDir}, filepath.Join(s.RepoDir, config.DefaultFileName)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		if verrs, ok := err.(*config.ValidationErrors); ok {
			return cfg, web.ConfigStatusVM{Source: path, Errors: verrs.Errors}, path
		}
		return nil, web.ConfigStatusVM{Source: path, Errors: []config.ValidationError{{Message: err.Error()}}}, path
	}
	return cfg, web.ConfigStatusVM{OK: true, Source: path}, path
}

func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, code int, msg string) {
	page := web.PageError{Base: s.base(w, r, msg), Code: code, Message: msg, HomeURL: "/"}
	s.Renderer.Render(w, code, "error.html", page)
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.errorPage(w, r, http.StatusNotFound, "Page not found")
}

func depURL(dep string) string            { return "/deployments/" + dep }
func targetURL(dep, target string) string { return depURL(dep) + "/targets/" + target }

// dep resolves the deployment named in the request path against the current
// configuration; it renders an error page and returns nil when unavailable.
func (s *Server) dep(w http.ResponseWriter, r *http.Request) (*config.Config, *config.Deployment) {
	cfg, status, _ := s.loadConfig()
	if cfg == nil || !status.OK {
		s.errorPage(w, r, http.StatusConflict, "The Keel configuration is missing or invalid — fix keel.yaml first")
		return nil, nil
	}
	d := cfg.Deployment(r.PathValue("dep"))
	if d == nil {
		s.errorPage(w, r, http.StatusNotFound, fmt.Sprintf("No deployment named %q in keel.yaml", r.PathValue("dep")))
		return nil, nil
	}
	return cfg, d
}

// target resolves the target named in the request path.
func (s *Server) target(w http.ResponseWriter, r *http.Request, d *config.Deployment) *devdb.Target {
	t, err := s.Q.GetTargetByName(r.Context(), devdb.GetTargetByNameParams{
		Deployment: d.Name,
		Name:       r.PathValue("target"),
	})
	if err != nil {
		s.errorPage(w, r, http.StatusNotFound, fmt.Sprintf("No target named %q for deployment %q", r.PathValue("target"), d.Name))
		return nil
	}
	return t
}

// targetValues decrypts the saved values for the declared variables of d.
// Secrets are returned in the values map too (the deploy path needs them);
// callers building form fields must use savedSecrets instead.
func (s *Server) targetValues(ctx context.Context, d *config.Deployment, targetID string) (values map[string]string, savedSecrets map[string]bool, err error) {
	rows, err := s.Q.ListTargetValues(ctx, targetID)
	if err != nil {
		return nil, nil, err
	}
	values = map[string]string{}
	savedSecrets = map[string]bool{}
	for _, row := range rows {
		v := d.Variable(row.VarName)
		if v == nil {
			continue // variable was removed from keel.yaml; ignore stale value
		}
		plain, err := s.Box.OpenString(row.ValueEnc)
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt %s: %w", row.VarName, err)
		}
		values[row.VarName] = plain
		if v.Secret {
			savedSecrets[row.VarName] = true
		}
	}
	return values, savedSecrets, nil
}

func millis(t time.Time) int64 { return t.UnixMilli() }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms) }

func fromMillisPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64)
	return &t
}

func intPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}

// runVM converts a stored run to its view model.
func runVM(run *devdb.Run) web.RunVM {
	status := run.Status
	return web.RunVM{
		ID:         run.ID,
		Deployment: run.Deployment,
		TargetName: run.TargetName,
		Status:     status,
		Trigger:    "manual",
		Error:      run.Error,
		ExitCode:   intPtr(run.ExitCode),
		FailedStep: intPtr(run.FailedStep),
		CreatedAt:  fromMillis(run.CreatedAt),
		StartedAt:  fromMillisPtr(run.StartedAt),
		FinishedAt: fromMillisPtr(run.FinishedAt),
		URL:        "/runs/" + run.ID,
		CancelURL:  "/runs/" + run.ID + "/cancel",
		Active:     status == "queued" || status == "building" || status == "running",
	}
}
