// Package skills embeds the agent skills Keel ships — SKILL.md documents
// that teach AI coding agents (Claude Code, Codex, Cursor, Gemini CLI,
// Copilot, …) how to author keel.yaml, environment images, and use the CLI.
//
// Each skill is a directory holding a SKILL.md with YAML frontmatter
// (name, description) and optional supporting files, following the Agent
// Skills format. `keel skills install` copies them into the agents' skill
// directories; the same directory layout lets other tooling discover them
// straight from the repository.
package skills

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed */SKILL.md */references
var content embed.FS

// FS returns the embedded skill files, rooted at the skills directory: one
// subdirectory per skill.
func FS() fs.FS { return content }

// Skill describes one shipped skill.
type Skill struct {
	// Name is the directory name and the frontmatter name.
	Name string
	// Description is the frontmatter description — what the skill covers
	// and when an agent should load it.
	Description string
	// Files are the skill's files, relative to the skill directory, sorted.
	Files []string
}

// All lists every embedded skill, sorted by name.
func All() ([]Skill, error) {
	entries, err := fs.ReadDir(content, ".")
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s := Skill{Name: e.Name()}
		if err := fs.WalkDir(content, s.Name, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				s.Files = append(s.Files, strings.TrimPrefix(p, s.Name+"/"))
			}
			return nil
		}); err != nil {
			return nil, err
		}
		sort.Strings(s.Files)
		raw, err := fs.ReadFile(content, s.Name+"/SKILL.md")
		if err != nil {
			return nil, err
		}
		s.Description = frontmatterField(string(raw), "description")
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Names returns the names of every embedded skill, sorted.
func Names() []string {
	all, err := All()
	if err != nil {
		return nil
	}
	names := make([]string, len(all))
	for i, s := range all {
		names[i] = s.Name
	}
	return names
}

// frontmatterField extracts a top-level scalar from a SKILL.md YAML
// frontmatter block. The frontmatter is written by hand in this repository
// and kept to simple `key: value` lines, so a line scan is enough.
func frontmatterField(doc, key string) string {
	if !strings.HasPrefix(doc, "---\n") {
		return ""
	}
	body := doc[4:]
	end := strings.Index(body, "\n---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(body[:end], "\n") {
		if v, ok := strings.CutPrefix(line, key+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
