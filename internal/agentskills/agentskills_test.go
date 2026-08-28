package agentskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UcGeorge/keel/skills"
)

func fakePaths(existing ...string) Paths {
	set := map[string]bool{}
	for _, e := range existing {
		set[e] = true
	}
	return Paths{
		Home:   "/home/u",
		Config: "/home/u/.config",
		Getenv: func(string) string { return "" },
		Exists: func(p string) bool { return set[filepath.ToSlash(p)] },
	}
}

func TestEmbeddedSkills(t *testing.T) {
	all, err := skills.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 3 {
		t.Fatalf("expected at least 3 skills, got %d", len(all))
	}
	for _, s := range all {
		if s.Description == "" {
			t.Errorf("%s: empty description", s.Name)
		}
		if len(s.Description) > 1024 {
			t.Errorf("%s: description is %d chars; the Agent Skills format caps it at 1024", s.Name, len(s.Description))
		}
		if len(s.Name) > 64 || strings.ToLower(s.Name) != s.Name {
			t.Errorf("%s: name must be lowercase and at most 64 chars", s.Name)
		}
		if s.Files[0] != "SKILL.md" && !contains(s.Files, "SKILL.md") {
			t.Errorf("%s: no SKILL.md", s.Name)
		}
		raw, _ := os.ReadFile(filepath.Join("..", "..", "skills", s.Name, "SKILL.md"))
		if !strings.Contains(string(raw), Marker) {
			t.Errorf("%s: SKILL.md lacks the %q marker install relies on", s.Name, Marker)
		}
		if !strings.Contains(string(raw), "name: "+s.Name+"\n") {
			t.Errorf("%s: frontmatter name must equal the directory name", s.Name)
		}
	}
}

func TestAgentsTable(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Agents {
		if seen[a.Key] {
			t.Errorf("duplicate agent key %q", a.Key)
		}
		seen[a.Key] = true
		if a.ProjectDir == "" || filepath.IsAbs(a.ProjectDir) || strings.Contains(a.ProjectDir, "..") {
			t.Errorf("%s: bad project dir %q", a.Key, a.ProjectDir)
		}
		if g := a.GlobalDir(fakePaths()); !filepath.IsAbs(g) {
			t.Errorf("%s: global dir %q is not absolute", a.Key, g)
		}
	}
	if Agents[len(Agents)-1].Key != UniversalKey {
		t.Error("universal agent must be listed last")
	}
}

func TestResolvePaths(t *testing.T) {
	p := fakePaths()
	cc := Find("claude-code")
	if got := cc.GlobalDir(p); filepath.ToSlash(got) != "/home/u/.claude/skills" {
		t.Errorf("claude-code global = %q", got)
	}
	p.Getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return "/custom/claude"
		}
		return ""
	}
	if got := cc.GlobalDir(p); filepath.ToSlash(got) != "/custom/claude/skills" {
		t.Errorf("claude-code global with CLAUDE_CONFIG_DIR = %q", got)
	}
	if got := Find("opencode").GlobalDir(fakePaths()); filepath.ToSlash(got) != "/home/u/.config/opencode/skills" {
		t.Errorf("opencode global = %q", got)
	}
}

func TestDetectAndResolve(t *testing.T) {
	p := fakePaths("/home/u/.claude", "/home/u/.codex", "/home/u/.cursor")
	det := Detect(p)
	var keys []string
	for _, a := range det {
		keys = append(keys, a.Key)
	}
	if strings.Join(keys, ",") != "claude-code,codex,cursor" {
		t.Fatalf("detected %v", keys)
	}

	targets, fallback, err := Resolve(nil, "/repo", false, p)
	if err != nil || fallback {
		t.Fatalf("resolve: %v fallback=%v", err, fallback)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 distinct dirs (claude + shared .agents), got %+v", targets)
	}
	if filepath.ToSlash(targets[0].Dir) != "/repo/.claude/skills" || targets[0].Agents[0] != "Claude Code" {
		t.Errorf("target 0 = %+v", targets[0])
	}
	if filepath.ToSlash(targets[1].Dir) != "/repo/.agents/skills" || len(targets[1].Agents) != 2 {
		t.Errorf("target 1 = %+v", targets[1])
	}

	targets, fallback, err = Resolve(nil, "/repo", false, fakePaths())
	if err != nil || !fallback || len(targets) != 1 || filepath.ToSlash(targets[0].Dir) != "/repo/.agents/skills" {
		t.Errorf("no agents: targets=%+v fallback=%v err=%v", targets, fallback, err)
	}

	targets, _, err = Resolve([]string{"all"}, "/repo", true, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) < 10 {
		t.Errorf("--agent all --global should yield many dirs, got %d", len(targets))
	}
	if _, _, err := Resolve([]string{"nope"}, "/repo", false, p); err == nil {
		t.Error("unknown agent must error")
	}
}

func TestInstallUpdateUninstall(t *testing.T) {
	dir := t.TempDir()
	names, err := Install(dir, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(skills.Names()) {
		t.Fatalf("installed %v", names)
	}
	if !IsManaged(filepath.Join(dir, "keel")) {
		t.Fatal("installed skill not recognized as managed")
	}
	if _, err := os.Stat(filepath.Join(dir, "keel", "references", "keel-yaml.md")); err != nil {
		t.Fatal("reference file missing:", err)
	}

	// A stale file from an older version disappears on reinstall.
	stale := filepath.Join(dir, "keel", "references", "old.md")
	os.WriteFile(stale, []byte("x"), 0o644)
	if _, err := Install(dir, []string{"keel"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale file survived reinstall")
	}

	// A foreign skill with the same name is protected unless forced.
	foreign := filepath.Join(dir, "keel-cli")
	os.RemoveAll(foreign)
	os.MkdirAll(foreign, 0o755)
	os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte("---\nname: keel-cli\n---\nmine"), 0o644)
	if _, err := Install(dir, []string{"keel-cli"}, false); err == nil {
		t.Error("expected refusal to overwrite a foreign skill")
	}
	if _, err := Uninstall(dir, []string{"keel-cli"}); err == nil {
		t.Error("expected refusal to remove a foreign skill")
	}
	if _, err := Install(dir, []string{"keel-cli"}, true); err != nil {
		t.Fatal("force install:", err)
	}
	if !IsManaged(foreign) {
		t.Error("forced install did not replace the skill")
	}

	removed, err := Uninstall(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != len(skills.Names()) {
		t.Errorf("removed %v", removed)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("directory not empty after uninstall: %v", entries)
	}
	if _, err := Install(dir, []string{"bogus"}, false); err == nil {
		t.Error("unknown skill must error")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
