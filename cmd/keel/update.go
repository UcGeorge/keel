package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/UcGeorge/keel/internal/selfupdate"
	"github.com/UcGeorge/keel/internal/version"
	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	var check bool
	var target string
	var force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update keel to the latest release",
		Long: `Downloads the latest release of keel from GitHub, verifies its SHA-256
against the release's checksums.txt, and replaces this executable in place.

keel also checks for a new release at most once a day — while a command
runs, waiting at most two seconds for the answer — and mentions it after the
command; set KEEL_NO_UPDATE_CHECK=1 to turn that off.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			current := version.Version
			if !selfupdate.IsRelease(current) && !force {
				return fmt.Errorf("this keel is a development build (version %s), not a release — rebuild it from source, or pass --force to replace it with the latest release", current)
			}
			latest := target
			if latest == "" {
				var err error
				latest, err = selfupdate.LatestTag(cmd.Context(), selfupdate.DefaultBaseURL)
				if err != nil {
					return err
				}
			} else if !strings.HasPrefix(latest, "v") {
				latest = "v" + latest
			}
			fmt.Printf("Current version: %s\n", current)
			if target == "" {
				fmt.Printf("Latest release:  %s\n", latest)
			} else {
				fmt.Printf("Requested:       %s\n", latest)
			}
			if latest == current {
				fmt.Println("keel is up to date.")
				return nil
			}
			if target == "" && !selfupdate.Newer(latest, current) {
				fmt.Println("keel is up to date.")
				return nil
			}
			if check {
				fmt.Println("Run `keel update` to install it.")
				return nil
			}
			res, err := selfupdate.Update(cmd.Context(), selfupdate.Options{
				Version: latest,
				Log:     func(s string) { fmt.Println(s) },
			})
			if err != nil {
				return err
			}
			// Remember the answer so the background check stays quiet.
			_ = selfupdate.WriteState(selfupdate.StatePath(), selfupdate.State{CheckedAt: time.Now(), Latest: res.Version})
			fmt.Printf("Updated %s to %s\n", res.Executable, res.Version)
			if out, err := exec.Command(res.Executable, "--version").Output(); err == nil {
				fmt.Printf("Now running: %s\n", strings.TrimSpace(string(out)))
			} else {
				fmt.Fprintf(os.Stderr, "⚠ could not run the new binary: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().StringVar(&target, "version", "", "install this release tag instead of the latest (allows downgrades)")
	cmd.Flags().BoolVar(&force, "force", false, "replace a development build with a release")
	return cmd
}
