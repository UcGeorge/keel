// Package web holds everything the Keel Dev and Keel Cloud UIs share:
// embedded templates and static assets, the page renderer, view models,
// flash messages, and SSE helpers. Both UIs are server-rendered from the
// same template set, so they look and behave identically.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:templates static
var assets embed.FS

// Renderer renders pages. Every page template is parsed together with the
// layout and all partials at construction time, so template errors surface
// at startup, never per-request.
type Renderer struct {
	pages map[string]*template.Template
}

// NewRenderer parses all embedded templates.
func NewRenderer() (*Renderer, error) {
	partials, err := fs.Glob(assets, "templates/partials/*.html")
	if err != nil {
		return nil, err
	}
	r := &Renderer{pages: map[string]*template.Template{}}
	err = fs.WalkDir(assets, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		rel := strings.TrimPrefix(p, "templates/")
		if rel == "layout.html" || strings.HasPrefix(rel, "partials/") {
			return nil
		}
		files := append([]string{"templates/layout.html"}, partials...)
		files = append(files, p)
		t, err := template.New("layout.html").Funcs(Funcs).ParseFS(assets, files...)
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		r.pages[rel] = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(r.pages) == 0 {
		return nil, fmt.Errorf("no page templates found")
	}
	return r, nil
}

// Render writes a full page. Pages are rendered to a buffer first so a
// template error can never emit a half-written response.
func (r *Renderer) Render(w http.ResponseWriter, status int, page string, data any) {
	t, ok := r.pages[page]
	if !ok {
		slog.Error("unknown template page", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		slog.Error("render page", "page", page, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// RenderFragment writes one named sub-template of a page (an htmx partial
// response or an SSE payload), buffered like Render.
func (r *Renderer) RenderFragment(w http.ResponseWriter, page, name string, data any) {
	html, err := r.FragmentHTML(page, name, data)
	if err != nil {
		slog.Error("render fragment", "page", page, "name", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}

// FragmentHTML renders one named sub-template of a page to a string.
func (r *Renderer) FragmentHTML(page, name string, data any) (string, error) {
	t, ok := r.pages[page]
	if !ok {
		return "", fmt.Errorf("unknown template page %q", page)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// StaticHandler serves the embedded static assets under /static/.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Immutable-ish caching; assets change only with a new binary.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, req)
	})
}

// IsHTMX reports whether the request came from htmx.
func IsHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// Flash is a one-shot notification shown on the next rendered page.
type Flash struct {
	Kind    string // "success" | "error" | "info"
	Message string
}

const flashCookie = "keel_flash"

// SetFlash stores a flash message for the next page load.
func SetFlash(w http.ResponseWriter, kind, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    encodeFlash(kind, message),
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// TakeFlash reads and clears the pending flash message, if any.
func TakeFlash(w http.ResponseWriter, r *http.Request) *Flash {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	kind, msg, ok := decodeFlash(c.Value)
	if !ok {
		return nil
	}
	return &Flash{Kind: kind, Message: msg}
}

func encodeFlash(kind, message string) string {
	return kind + "|" + strings.ReplaceAll(template.URLQueryEscaper(message), "|", "%7C")
}

func decodeFlash(v string) (kind, msg string, ok bool) {
	kind, rest, found := strings.Cut(v, "|")
	if !found {
		return "", "", false
	}
	unescaped, err := urlUnescape(rest)
	if err != nil {
		return "", "", false
	}
	return kind, unescaped, true
}

// Base carries the fields every page needs. Page data structs embed it so
// the layout can reach {{.Title}}, {{.Flash}}, {{.CSRF}}, and navigation.
type Base struct {
	// Title is the page title (browser tab and page heading context).
	Title string
	// AppName is "Keel" (dev) or "Keel Cloud".
	AppName string
	// Mode is "dev" or "cloud"; the layout adapts navigation to it.
	Mode string
	// Nav is the primary navigation.
	Nav []NavItem
	// ContextName names what the nav is scoped to (repo dir or org name).
	ContextName string
	// User, when set, renders the account menu (cloud).
	User *UserInfo
	// Orgs is the organization switcher content (cloud).
	Orgs []OrgNavItem
	// OrgSlug is the active organization slug (cloud).
	OrgSlug string
	// Flash is the pending one-shot notification.
	Flash *Flash
	// CSRF is the session's CSRF token, injected into every form and htmx
	// request from the layout.
	CSRF string
	// Version is the Keel build version, shown in the footer.
	Version string
}

// NavItem is one primary-navigation entry.
type NavItem struct {
	Label  string
	Href   string
	Active bool
}

// UserInfo is the signed-in user shown in the header (cloud).
type UserInfo struct {
	Name  string
	Email string
}

// OrgNavItem is one entry in the organization switcher (cloud).
type OrgNavItem struct {
	Name     string
	Slug     string
	Personal bool
	Active   bool
}

// SSE is a minimal server-sent-events writer.
type SSE struct {
	w http.ResponseWriter
	f http.Flusher
}

// NewSSE prepares the response for an SSE stream. It returns an error when
// the connection cannot stream.
func NewSSE(w http.ResponseWriter) (*SSE, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &SSE{w: w, f: f}, nil
}

// Event writes one event. Multi-line data is split into multiple data:
// fields per the SSE format. id may be empty.
func (s *SSE) Event(id, event, data string) error {
	var b strings.Builder
	if id != "" {
		fmt.Fprintf(&b, "id: %s\n", id)
	}
	if event != "" {
		fmt.Fprintf(&b, "event: %s\n", event)
	}
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	if _, err := fmt.Fprint(s.w, b.String()); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// Comment writes an SSE comment, useful as a keep-alive.
func (s *SSE) Comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// LogLineHTML formats one log line as the HTML fragment appended to the
// run-page log pane (matching the initial server render in run.html).
func LogLineHTML(line string) string {
	return `<div class="whitespace-pre-wrap break-all">` + template.HTMLEscapeString(line) + `</div>`
}

// CleanPath normalizes a request path for comparisons.
func CleanPath(p string) string {
	if p == "" {
		return "/"
	}
	c := path.Clean(p)
	if c == "." {
		return "/"
	}
	return c
}

// Since is a tiny helper for handlers measuring durations.
func Since(t time.Time) time.Duration { return time.Since(t) }
