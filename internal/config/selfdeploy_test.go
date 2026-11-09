package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual rollout step with SSH already available. The fake
// server refuses deployment until its first-boot readiness check succeeds.
func TestCloudRolloutWaitsForBootstrap(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("shell unavailable")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "keel.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var script string
	for _, step := range cfg.Deployment("cloud-gcp").Steps {
		if step.Name == "Roll out on the server" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("cloud-gcp rollout step missing")
	}
	for _, tc := range []struct {
		name, bootExit, mode string
		wantSuccess, deploy  bool
	}{
		{"ready", "0", "full", true, true},
		{"bootstrap failed", "1", "full", false, false},
		{"bootstrap reported errors", "2", "full", false, false},
		{"bootstrap timed out", "124", "full", false, false},
		{"plan skips rollout", "0", "plan", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			const gcloud = `#!/bin/sh
set -eu
remote=
while [ "$#" -gt 0 ]; do
  if [ "$1" = --command ]; then
    shift
    remote="$1"
  fi
  shift
done
case "$remote" in
  true) exit 0 ;;
  *"cloud-init status --wait"*)
    if [ "$BOOT_EXIT" = 0 ]; then
      touch "$TEST_DIR/ready"
    fi
    exit "$BOOT_EXIT"
    ;;
  *"/opt/keel/rollout.sh"*)
    test -f "$TEST_DIR/ready" || { echo "rollout raced first boot" >&2; exit 1; }
    touch "$TEST_DIR/deployed"
    ;;
  *) echo "unexpected SSH command" >&2; exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "gcloud"), []byte(gcloud), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(shell, "-eu", "-c", script)
			cmd.Env = append(os.Environ(),
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_DIR="+dir, "BOOT_EXIT="+tc.bootExit, "ACTION=deploy", "RUN_MODE="+tc.mode,
				"GCP_ZONE=us-central1-a", "IMAGE=example.test/keel:audit",
			)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("success=%v, want %v: %s", err == nil, tc.wantSuccess, output)
			}
			_, err = os.Stat(filepath.Join(dir, "deployed"))
			if deployed := err == nil; deployed != tc.deploy {
				t.Fatalf("deployed=%v, want %v: %s", deployed, tc.deploy, output)
			}
			if !tc.wantSuccess && !strings.Contains(string(output), "Server setup did not complete") {
				t.Fatalf("missing actionable setup failure: %s", output)
			}
		})
	}
}
