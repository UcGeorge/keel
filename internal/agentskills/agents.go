// Package agentskills installs the skills Keel ships into the skill
// directories of AI coding agents.
//
// Every supported agent reads Agent Skills (a directory per skill holding a
// SKILL.md) from a project-level directory relative to the repository and
// from a global directory under the user's home. Several agents share the
// project-level `.agents/skills` convention, so one copy serves them all.
package agentskills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Agent is one supported AI coding agent.
type Agent struct {
	// Key is the identifier accepted by --agent.
	Key string
	// Name is the display name.
	Name string
	// ProjectDir is the skills directory relative to the repository root.
	ProjectDir string

	global dir
	detect []dir
}

// dir locates a directory under the user's home or config directory,
// optionally overridden by an environment variable naming the base.
type dir struct {
	env      string // environment variable holding the base directory, if any
	fallback string // base when env is unset: "~/x" (home) or "$config/x" (config dir)
	sub      string // appended to the base
}

// Paths is the environment path resolution runs against.
type Paths struct {
	// Home is the user's home directory.
	Home string
	// Config is $XDG_CONFIG_HOME, defaulting to Home/.config.
	Config string
	// Getenv looks up environment variables.
	Getenv func(string) string
	// Exists reports whether a path exists.
	Exists func(string) bool
}

// DefaultPaths resolves against the current process environment.
func DefaultPaths() Paths {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return Paths{
		Home:   home,
		Config: cfg,
		Getenv: os.Getenv,
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
	}
}

func (p Paths) resolve(d dir) string {
	base := ""
	if d.env != "" {
		base = p.Getenv(d.env)
	}
	if base == "" {
		switch {
		case strings.HasPrefix(d.fallback, "~/"):
			base = filepath.Join(p.Home, filepath.FromSlash(d.fallback[2:]))
		case strings.HasPrefix(d.fallback, "$config/"):
			base = filepath.Join(p.Config, filepath.FromSlash(strings.TrimPrefix(d.fallback, "$config/")))
		default:
			base = filepath.FromSlash(d.fallback)
		}
	}
	if d.sub == "" {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(d.sub))
}

// GlobalDir is the agent's user-wide skills directory.
func (a Agent) GlobalDir(p Paths) string { return p.resolve(a.global) }

// Detected reports whether the agent appears to be installed on this
// machine — one of its configuration directories exists.
func (a Agent) Detected(p Paths) bool {
	for _, d := range a.detect {
		if p.Exists(p.resolve(d)) {
			return true
		}
	}
	return false
}

// UniversalKey is the pseudo-agent that stands for the shared
// `.agents/skills` convention; it is never detected and is the fallback
// when no agent is.
const UniversalKey = "universal"

// home and config are shorthands for the two fallback bases.
func home(rel string) string   { return "~/" + rel }
func config(rel string) string { return "$config/" + rel }

// Agents lists every supported agent, sorted by display name, with the
// universal entry last.
var Agents = func() []Agent {
	list := []Agent{
		{Key: "amp", Name: "Amp", ProjectDir: ".agents/skills",
			global: dir{fallback: config("agents"), sub: "skills"},
			detect: []dir{{fallback: config("amp")}}},
		{Key: "antigravity", Name: "Antigravity", ProjectDir: ".agents/skills",
			global: dir{fallback: home(".gemini/antigravity"), sub: "skills"},
			detect: []dir{{fallback: home(".gemini/antigravity")}}},
		{Key: "augment", Name: "Augment", ProjectDir: ".augment/skills",
			global: dir{fallback: home(".augment"), sub: "skills"},
			detect: []dir{{fallback: home(".augment")}}},
		{Key: "claude-code", Name: "Claude Code", ProjectDir: ".claude/skills",
			global: dir{env: "CLAUDE_CONFIG_DIR", fallback: home(".claude"), sub: "skills"},
			detect: []dir{{env: "CLAUDE_CONFIG_DIR", fallback: home(".claude")}}},
		{Key: "cline", Name: "Cline", ProjectDir: ".agents/skills",
			global: dir{fallback: home(".agents"), sub: "skills"},
			detect: []dir{{fallback: home(".cline")}}},
		{Key: "codex", Name: "Codex", ProjectDir: ".agents/skills",
			global: dir{env: "CODEX_HOME", fallback: home(".codex"), sub: "skills"},
			detect: []dir{{env: "CODEX_HOME", fallback: home(".codex")}}},
		{Key: "continue", Name: "Continue", ProjectDir: ".continue/skills",
			global: dir{fallback: home(".continue"), sub: "skills"},
			detect: []dir{{fallback: home(".continue")}}},
		{Key: "cursor", Name: "Cursor", ProjectDir: ".agents/skills",
			global: dir{fallback: home(".cursor"), sub: "skills"},
			detect: []dir{{fallback: home(".cursor")}}},
		{Key: "droid", Name: "Droid (Factory)", ProjectDir: ".factory/skills",
			global: dir{fallback: home(".factory"), sub: "skills"},
			detect: []dir{{fallback: home(".factory")}}},
		{Key: "gemini-cli", Name: "Gemini CLI", ProjectDir: ".agents/skills",
			global: dir{fallback: home(".gemini"), sub: "skills"},
			detect: []dir{{fallback: home(".gemini")}}},
		{Key: "github-copilot", Name: "GitHub Copilot", ProjectDir: ".agents/skills",
			global: dir{fallback: home(".copilot"), sub: "skills"},
			detect: []dir{{fallback: home(".copilot")}}},
		{Key: "goose", Name: "Goose", ProjectDir: ".goose/skills",
			global: dir{fallback: config("goose"), sub: "skills"},
			detect: []dir{{fallback: config("goose")}}},
		{Key: "junie", Name: "Junie", ProjectDir: ".junie/skills",
			global: dir{fallback: home(".junie"), sub: "skills"},
			detect: []dir{{fallback: home(".junie")}}},
		{Key: "kilo", Name: "Kilo Code", ProjectDir: ".kilocode/skills",
			global: dir{fallback: home(".kilocode"), sub: "skills"},
			detect: []dir{{fallback: home(".kilocode")}}},
		{Key: "kiro-cli", Name: "Kiro CLI", ProjectDir: ".kiro/skills",
			global: dir{fallback: home(".kiro"), sub: "skills"},
			detect: []dir{{fallback: home(".kiro")}}},
		{Key: "opencode", Name: "OpenCode", ProjectDir: ".agents/skills",
			global: dir{fallback: config("opencode"), sub: "skills"},
			detect: []dir{{fallback: config("opencode")}}},
		{Key: "qwen-code", Name: "Qwen Code", ProjectDir: ".qwen/skills",
			global: dir{fallback: home(".qwen"), sub: "skills"},
			detect: []dir{{fallback: home(".qwen")}}},
		{Key: "roo", Name: "Roo Code", ProjectDir: ".roo/skills",
			global: dir{fallback: home(".roo"), sub: "skills"},
			detect: []dir{{fallback: home(".roo")}}},
		{Key: "warp", Name: "Warp", ProjectDir: ".agents/skills",
			global: dir{fallback: home(".agents"), sub: "skills"},
			detect: []dir{{fallback: home(".warp")}}},
		{Key: "windsurf", Name: "Windsurf", ProjectDir: ".windsurf/skills",
			global: dir{fallback: home(".codeium/windsurf"), sub: "skills"},
			detect: []dir{{fallback: home(".codeium/windsurf")}}},
		{Key: "zed", Name: "Zed", ProjectDir: ".agents/skills",
			global: dir{fallback: home(".agents"), sub: "skills"},
			detect: []dir{{fallback: config("zed")}, {env: "APPDATA", fallback: "", sub: "Zed"}}},
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return append(list, Agent{
		Key: UniversalKey, Name: "Any agent (.agents/skills)", ProjectDir: ".agents/skills",
		global: dir{fallback: config("agents"), sub: "skills"},
	})
}()

// Find returns the agent with the given key, or nil.
func Find(key string) *Agent {
	for i := range Agents {
		if Agents[i].Key == key {
			return &Agents[i]
		}
	}
	return nil
}

// Keys returns every agent key in listing order.
func Keys() []string {
	keys := make([]string, len(Agents))
	for i, a := range Agents {
		keys[i] = a.Key
	}
	return keys
}

// Detect returns the agents installed on this machine, in listing order.
func Detect(p Paths) []Agent {
	var out []Agent
	for _, a := range Agents {
		if a.Detected(p) {
			out = append(out, a)
		}
	}
	return out
}
