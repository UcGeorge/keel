package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/smart-minds/keel/internal/config"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// md renders trusted-authored markdown (descriptions, manifest text) to
// HTML. Raw HTML in the source is omitted by goldmark's default policy, so
// the output is safe to inline.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// Funcs is the template function map shared by every page.
var Funcs = template.FuncMap{
	"markdown": func(src string) template.HTML {
		var buf bytes.Buffer
		if err := md.Convert([]byte(src), &buf); err != nil {
			return template.HTML(template.HTMLEscapeString(src))
		}
		return template.HTML(buf.String())
	},
	"json": func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			return "null"
		}
		return string(b)
	},
	"timeago": TimeAgo,
	"deref": func(t *time.Time) time.Time {
		if t == nil {
			return time.Time{}
		}
		return *t
	},
	"datefmt":  func(t time.Time) string { return t.Local().Format("Jan 2, 2006 15:04") },
	"duration": HumanDuration,
	"lower":    strings.ToLower,
	"add":      func(a, b int) int { return a + b },
	"dict": func(pairs ...any) (map[string]any, error) {
		if len(pairs)%2 != 0 {
			return nil, fmt.Errorf("dict requires an even number of arguments")
		}
		m := make(map[string]any, len(pairs)/2)
		for i := 0; i < len(pairs); i += 2 {
			k, ok := pairs[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict keys must be strings")
			}
			m[k] = pairs[i+1]
		}
		return m, nil
	},
	"initials": func(name string) string {
		fields := strings.Fields(name)
		if len(fields) == 0 {
			return "?"
		}
		out := string([]rune(fields[0])[0:1])
		if len(fields) > 1 {
			out += string([]rune(fields[len(fields)-1])[0:1])
		}
		return strings.ToUpper(out)
	},
	"truncate": func(n int, s string) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n-1]) + "…"
	},
	"shortsha": func(s string) string {
		if len(s) > 7 {
			return s[:7]
		}
		return s
	},
	"statusdot":   StatusDot,
	"statusbadge": StatusBadge,
	"typelabel": func(t config.VarType) string {
		switch t {
		case config.VarText:
			return "Text"
		case config.VarMultiline:
			return "Multi-line"
		case config.VarNumber:
			return "Number"
		case config.VarEmail:
			return "Email"
		case config.VarURL:
			return "URL"
		case config.VarBoolean:
			return "Boolean"
		case config.VarSelect:
			return "Select"
		}
		return string(t)
	},
}

// TimeAgo renders a compact relative time ("3m ago", "2h ago", "5d ago").
func TimeAgo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Local().Format("Jan 2, 2006")
	}
}

// HumanDuration renders a run duration ("42s", "3m 12s", "1h 4m").
func HumanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// StatusDot returns the color classes for a status indicator dot.
func StatusDot(status string) string {
	switch status {
	case "succeeded", "ok":
		return "bg-emerald-500"
	case "failed", "error", "config_invalid", "config_missing":
		return "bg-red-500"
	case "running", "building":
		return "bg-sky-500 animate-pulse"
	case "queued", "pending":
		return "bg-amber-400"
	case "canceled":
		return "bg-slate-400"
	case "skipped":
		return "bg-slate-300"
	}
	return "bg-slate-400"
}

// StatusBadge returns the pill classes for a status badge.
func StatusBadge(status string) string {
	base := "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium "
	switch status {
	case "succeeded", "ok":
		return base + "bg-emerald-50 text-emerald-700 ring-1 ring-emerald-600/20"
	case "failed", "error", "config_invalid", "config_missing":
		return base + "bg-red-50 text-red-700 ring-1 ring-red-600/20"
	case "running", "building":
		return base + "bg-sky-50 text-sky-700 ring-1 ring-sky-600/20"
	case "queued", "pending":
		return base + "bg-amber-50 text-amber-700 ring-1 ring-amber-600/20"
	default:
		return base + "bg-slate-100 text-slate-600 ring-1 ring-slate-500/20"
	}
}

func urlUnescape(s string) (string, error) { return url.QueryUnescape(s) }
