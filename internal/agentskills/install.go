package agentskills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/UcGeorge/keel/skills"
)

// Marker identifies a SKILL.md written by Keel. Install replaces skill
// directories carrying it and refuses to touch any other, so a team's own
// skill that happens to share a name is never overwritten by accident.
const Marker = "author: keel"

// Target is one directory to install into and the agents that read it.
type Target struct {
	// Dir is the absolute skills directory.
	Dir string
	// Agents are the display names of the agents sharing Dir.
	Agents []string
}

// Resolve turns an agent selection into the distinct directories to
// install into. keys are agent keys, or "all"; with none given, the
// detected agents are used, falling back to the universal directory when
// nothing is detected. repoDir is the repository root for project-level
// installs; with global set, each agent's user-wide directory is used.
// The second result reports whether the fallback was taken.
func Resolve(keys []string, repoDir string, global bool, p Paths) ([]Target, bool, error) {
	var agents []Agent
	fallback := false
	switch {
	case len(keys) == 1 && keys[0] == "all":
		for _, a := range Agents {
			if a.Key != UniversalKey {
				agents = append(agents, a)
			}
		}
	case len(keys) > 0:
		for _, k := range keys {
			a := Find(k)
			if a == nil {
				return nil, false, fmt.Errorf("unknown agent %q (run `keel skills agents` for the list)", k)
			}
			agents = append(agents, *a)
		}
	default:
		agents = Detect(p)
		if len(agents) == 0 {
			agents = []Agent{*Find(UniversalKey)}
			fallback = true
		}
	}

	byDir := map[string]*Target{}
	var order []string
	for _, a := range agents {
		d := a.GlobalDir(p)
		if !global {
			d = filepath.Join(repoDir, filepath.FromSlash(a.ProjectDir))
		}
		t, ok := byDir[d]
		if !ok {
			t = &Target{Dir: d}
			byDir[d] = t
			order = append(order, d)
		}
		t.Agents = append(t.Agents, a.Name)
	}
	out := make([]Target, 0, len(order))
	for _, d := range order {
		out = append(out, *byDir[d])
	}
	return out, fallback, nil
}

// Install writes the named skills (all of them when names is empty) into
// dir, one subdirectory per skill. A skill directory that already exists
// is replaced when Keel wrote it (see Marker) or when force is set, and
// left alone with an error otherwise. It returns the installed skill names.
func Install(dir string, names []string, force bool) ([]string, error) {
	selected, err := selectSkills(names)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var installed []string
	for _, s := range selected {
		dst := filepath.Join(dir, s.Name)
		if err := clearManaged(dst, force); err != nil {
			return installed, err
		}
		for _, f := range s.Files {
			data, err := fs.ReadFile(skills.FS(), s.Name+"/"+f)
			if err != nil {
				return installed, err
			}
			path := filepath.Join(dst, filepath.FromSlash(f))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return installed, err
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return installed, err
			}
		}
		installed = append(installed, s.Name)
	}
	return installed, nil
}

// Uninstall removes the named skills (all when empty) from dir, touching
// only directories Keel wrote. It returns the names actually removed.
func Uninstall(dir string, names []string) ([]string, error) {
	selected, err := selectSkills(names)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, s := range selected {
		dst := filepath.Join(dir, s.Name)
		if _, err := os.Stat(dst); err != nil {
			continue
		}
		if !IsManaged(dst) {
			return removed, fmt.Errorf("%s was not installed by Keel — remove it by hand if you mean to", dst)
		}
		if err := os.RemoveAll(dst); err != nil {
			return removed, err
		}
		removed = append(removed, s.Name)
	}
	return removed, nil
}

// IsManaged reports whether the skill directory holds a SKILL.md written
// by Keel.
func IsManaged(skillDir string) bool {
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), Marker)
}

// clearManaged removes an existing skill directory so a fresh copy can be
// written, refusing directories Keel does not own unless force is set.
func clearManaged(dst string, force bool) error {
	info, err := os.Stat(dst)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", dst)
	}
	if !force && !IsManaged(dst) {
		return fmt.Errorf("%s exists and was not installed by Keel — use --force to replace it", dst)
	}
	return os.RemoveAll(dst)
}

func selectSkills(names []string) ([]skills.Skill, error) {
	all, err := skills.All()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return all, nil
	}
	byName := map[string]skills.Skill{}
	for _, s := range all {
		byName[s.Name] = s
	}
	var out []skills.Skill
	seen := map[string]bool{}
	for _, n := range names {
		s, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("unknown skill %q (have: %s)", n, strings.Join(skills.Names(), ", "))
		}
		if !seen[n] {
			out = append(out, s)
			seen[n] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
