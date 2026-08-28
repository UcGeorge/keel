// Command keel is the Keel CLI: initialize a repository, validate its
// configuration, run the local UI, deploy from the terminal, and export
// variable manifests.
package main

import (
	"fmt"
	"os"

	"github.com/smart-minds/keel/internal/version"
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
	root.AddCommand(
		newInitCmd(),
		newValidateCmd(),
		newDevCmd(),
		newDeployCmd(),
		newManifestCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
