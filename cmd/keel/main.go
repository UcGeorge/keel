// Command keel is the Keel CLI: initialize a repository, validate its
// configuration, run the local UI, deploy from the terminal, export
// variable manifests, install Keel skills into AI coding agents, and update
// itself.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/UcGeorge/keel/internal/selfupdate"
	"github.com/UcGeorge/keel/internal/version"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "keel",
		Short: "Keel — deployments your whole team can run",
		Long: `Keel turns the tribal knowledge of "how we deploy" into a keel.yaml every
team member (and every client-facing operator) can use: deployments run in a
Docker-defined environment, variables are declared once and filled per
target, and a variable manifest can be exported for whoever must supply the
values.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Version,
	}

	// Look for a newer release while the command runs — at most once a
	// day — and mention it once the command is done. Quick commands finish
	// before the request does, so the post-run hook grants it a short,
	// bounded wait; on other days the saved answer is used instantly.
	var check *selfupdate.Check
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if cmd.Name() != "update" {
			check = selfupdate.Start(version.Version)
		}
	}
	root.PersistentPostRun = func(cmd *cobra.Command, args []string) {
		if msg := check.Notice(2 * time.Second); msg != "" {
			fmt.Fprintf(os.Stderr, "\n%s\n", msg)
		}
	}
	root.AddCommand(
		newInitCmd(),
		newValidateCmd(),
		newDevCmd(),
		newDeployCmd(),
		newManifestCmd(),
		newSkillsCmd(),
		newUpdateCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
