package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/UcGeorge/keel/internal/config"
	"github.com/UcGeorge/keel/internal/manifest"
)

// ManifestRequest carries everything the shared manifest-builder UI needs.
type ManifestRequest struct {
	Base       Base
	Dep        *config.Deployment
	DepURL     string
	TargetName string
	Project    string
	PreparedBy string
	FormAction string
	BackURL    string
}

// ServeManifestBuilder implements the manifest builder page and its
// downloads for both Keel Dev and Keel Cloud.
//
// Query parameters:
//
//	var=NAME   (repeated) — selected variables; absent on first load, where
//	           the deployment's default selection applies
//	sel=1      — marks a form submission, so zero checkboxes means "none"
//	download=  — "md" or "html" to download instead of preview
func ServeManifestBuilder(renderer *Renderer, w http.ResponseWriter, r *http.Request, req ManifestRequest) {
	q := r.URL.Query()
	var selected []string
	if q.Get("sel") == "1" {
		selected = q["var"]
		if selected == nil {
			selected = []string{}
		}
	} else if q["var"] != nil {
		selected = q["var"]
	}
	if selected != nil {
		selected = manifest.SortSelection(req.Dep, selected)
	}

	opts := manifest.Options{
		Select:      selected,
		ProjectName: req.Project,
		TargetName:  req.TargetName,
		PreparedBy:  req.PreparedBy,
	}
	doc, genErr := manifest.Generate(req.Dep, opts)

	if dl := q.Get("download"); dl != "" && genErr == nil {
		name := "keel-manifest-" + req.Dep.Name
		if req.TargetName != "" {
			name += "-" + req.TargetName
		}
		switch dl {
		case "md":
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".md"))
			_, _ = w.Write([]byte(doc))
			return
		case "html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".html"))
			project := req.Project
			if project == "" {
				project = req.Dep.Name
			}
			_, _ = w.Write([]byte(StandaloneManifestHTML(project, doc)))
			return
		}
	}

	page := PageManifest{
		Base:       req.Base,
		Dep:        NewDeploymentVM(req.Dep, req.DepURL),
		TargetName: req.TargetName,
		Selected:   map[string]bool{},
		FormAction: req.FormAction,
		BackURL:    req.BackURL,
	}
	effective := selected
	if effective == nil {
		effective = manifest.DefaultSelection(req.Dep)
	}
	for _, name := range effective {
		page.Selected[name] = true
	}
	if genErr != nil {
		page.Error = genErr.Error()
	} else {
		page.Markdown = doc
		page.Preview = MarkdownHTML(doc)
	}
	renderer.Render(w, http.StatusOK, "manifest.html", page)
}

// MarkdownHTML renders markdown to sanitized-by-omission HTML (goldmark's
// default policy drops raw HTML).
func MarkdownHTML(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}

// StandaloneManifestHTML wraps a generated manifest in a printable,
// self-contained HTML document.
func StandaloneManifestHTML(project, doc string) string {
	title := "Required values — " + project
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString("<title>")
	b.WriteString(template.HTMLEscapeString(title))
	b.WriteString("</title><style>")
	b.WriteString(`body{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:#334155;line-height:1.65;max-width:46rem;margin:0 auto;padding:3rem 1.5rem}
h1{font-size:1.5rem;color:#0f172a;letter-spacing:-.01em}
h2{font-size:1.1rem;color:#0f172a;margin-top:2rem}
code{background:#f1f5f9;border-radius:.25rem;padding:.1rem .3rem;font-size:.85em}
table{width:100%;border-collapse:collapse;margin:.75rem 0;font-size:.9em}
th,td{border:1px solid #e2e8f0;padding:.4rem .6rem;text-align:left}
th{background:#f8fafc}
hr{border:0;border-top:1px solid #e2e8f0;margin:1.5rem 0}
a{color:#0f766e}
@media print{body{padding:0}}`)
	b.WriteString("</style></head><body>")
	b.WriteString(string(MarkdownHTML(doc)))
	b.WriteString("</body></html>")
	return b.String()
}
