package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/UcGeorge/keel/internal/agentskills"
	"github.com/UcGeorge/keel/skills"
	"github.com/spf13/cobra"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install Keel skills into AI coding agents",
		Long: `Keel ships agent skills — SKILL.md documents that teach AI coding agents
(Claude Code, Codex, Cursor, Gemini CLI, GitHub Copilot, OpenCode, Windsurf,
and others) how to write a correct keel.yaml, build environment images, and
use the CLI. Install them once and your agent knows Keel.`,
	}
	cmd.AddCommand(newSkillsInstallCmd(), newSkillsUninstallCmd(), newSkillsListCmd(), newSkillsAgentsCmd())
	return cmd
}

// skillsTargetFlags are shared by install and uninstall.
type skillsTargetFlags struct {
	agents []string
	global bool
	to     string
	only   []string
}

func (f *skillsTargetFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringArrayVarP(&f.agents, "agent", "a", nil, "agent to target (repeatable; `all` for every supported agent; default: the agents detected on this machine)")
	cmd.Flags().BoolVarP(&f.global, "global", "g", false, "use the agents' user-wide skill directories instead of the repository")
	cmd.Flags().StringVar(&f.to, "to", "", "write into this directory instead of any agent's")
	cmd.Flags().StringArrayVar(&f.only, "skill", nil, "skill to include (repeatable; default: all)")
}

// targets resolves the directories a command acts on.
func (f *skillsTargetFlags) targets(args []string) ([]agentskills.Target, bool, error) {
	if f.to != "" {
		if len(f.agents) > 0 || f.global {
			return nil, false, fmt.Errorf("--to cannot be combined with --agent or --global")
		}
		abs, err := filepath.Abs(f.to)
		if err != nil {
			return nil, false, err
		}
		return []agentskills.Target{{Dir: abs, Agents: []string{"custom directory"}}}, false, nil
	}
	dir, err := workDir(args)
	if err != nil {
		return nil, false, err
	}
	return agentskills.Resolve(f.agents, dir, f.global, agentskills.DefaultPaths())
}

func newSkillsInstallCmd() *cobra.Command {
	var f skillsTargetFlags
	var force bool
	cmd := &cobra.Command{
		Use:   "install [dir]",
		Short: "Install the Keel skills into your agents' skill directories",
		Long: `Copies the Keel skills into the skill directory of each AI coding agent.

By default the agents installed on this machine are detected and the skills
go into their project-level directories under [dir] (default: the current
directory) — for example .claude/skills for Claude Code and .agents/skills
for Codex, Cursor, Gemini CLI, Copilot, and OpenCode. Commit those
directories and every teammate's agent gets the skills too.

Use --global to install into the agents' user-wide directories instead,
--agent to pick agents explicitly, or --to for any other directory. Running
the command again after upgrading keel refreshes the skills in place; skills
that Keel did not write are never overwritten without --force.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, fallback, err := f.targets(args)
			if err != nil {
				return err
			}
			if fallback {
				fmt.Println("No supported agent detected on this machine — installing into the shared .agents/skills directory, which most agents read.")
				fmt.Println("Run `keel skills agents` to see the supported agents, or pass --agent to choose.")
				fmt.Println()
			}
			var installed []string
			for _, t := range targets {
				names, err := agentskills.Install(t.Dir, f.only, force)
				if err != nil {
					return err
				}
				installed = names
				fmt.Printf("  %-28s %s\n", displayPath(t.Dir, args), strings.Join(t.Agents, ", "))
			}
			fmt.Printf("\nInstalled %d skill(s): %s\n", len(installed), strings.Join(installed, ", "))
			if !f.global && f.to == "" {
				fmt.Println("Commit these directories so your teammates' agents get the skills too.")
			}
			fmt.Println("Re-run `keel skills install` after upgrading keel to refresh them.")
			return nil
		},
	}
	f.bind(cmd)
	cmd.Flags().BoolVar(&force, "force", false, "replace skill directories that Keel did not write")
	return cmd
}

func newSkillsUninstallCmd() *cobra.Command {
	var f skillsTargetFlags
	cmd := &cobra.Command{
		Use:   "uninstall [dir]",
		Short: "Remove the Keel skills from your agents' skill directories",
		Long: `Removes the Keel skills from the same directories install writes to, with the
same --agent, --global, --to, and --skill selection. Only directories Keel
wrote are removed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, _, err := f.targets(args)
			if err != nil {
				return err
			}
			total := 0
			for _, t := range targets {
				removed, err := agentskills.Uninstall(t.Dir, f.only)
				if err != nil {
					return err
				}
				if len(removed) == 0 {
					continue
				}
				total += len(removed)
				fmt.Printf("  %-28s removed %s\n", displayPath(t.Dir, args), strings.Join(removed, ", "))
			}
			if total == 0 {
				fmt.Println("Nothing to remove — no Keel skills found in the selected directories.")
			}
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

func newSkillsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show the skills Keel ships",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			all, err := skills.All()
			if err != nil {
				return err
			}
			for i, s := range all {
				if i > 0 {
					fmt.Println()
				}
				fmt.Printf("%s\n  %s\n  files: %s\n", s.Name, s.Description, strings.Join(s.Files, ", "))
			}
			return nil
		},
	}
}

func newSkillsAgentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List the supported agents, their skill directories, and which are installed here",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := agentskills.DefaultPaths()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "AGENT\tNAME\tDETECTED\tPROJECT DIRECTORY\tGLOBAL DIRECTORY")
			for _, a := range agentskills.Agents {
				detected := ""
				if a.Detected(p) {
					detected = "✓"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", a.Key, a.Name, detected, a.ProjectDir, abbreviateHome(a.GlobalDir(p), p.Home))
			}
			return w.Flush()
		},
	}
}

// displayPath shows a target directory relative to the repository when it
// is inside it, which is the common case, and absolute otherwise.
func displayPath(dir string, args []string) string {
	root, err := workDir(args)
	if err != nil {
		return dir
	}
	if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abbreviateHome(dir, "")
}

func abbreviateHome(p, home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
