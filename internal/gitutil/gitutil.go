// Package gitutil clones repositories for Keel Cloud using the git CLI.
// Credentials travel via GIT_CONFIG_* environment variables, never argv,
// so they are not visible in the host process list.
package gitutil

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Auth is HTTPS basic authentication for a clone. For GitHub App
// installation tokens use Username "x-access-token".
type Auth struct {
	Username string
	Password string
}

// env renders the credential header configuration.
func (a *Auth) env() []string {
	if a == nil || a.Password == "" {
		return nil
	}
	user := a.Username
	if user == "" {
		user = "token"
	}
	cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + a.Password))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + cred,
	}
}

// CheckGit verifies the git CLI is available.
func CheckGit() error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git is not installed or not in PATH")
	}
	return nil
}

// CloneShallow clones one branch of url at depth 1 into dir (which must not
// exist yet) and returns the checked-out commit SHA.
func CloneShallow(ctx context.Context, url, branch, dir string, auth *Auth) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	args := []string{"clone", "--depth", "1", "--single-branch"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", url, dir)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	cmd.Env = append(cmd.Env, auth.env()...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone failed: %s", cleanGitError(out.String()))
	}

	rev := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	shaOut, err := rev.Output()
	if err != nil {
		return "", fmt.Errorf("read HEAD: %w", err)
	}
	return strings.TrimSpace(string(shaOut)), nil
}

// cleanGitError trims noise and any leaked URL credentials from git output.
func cleanGitError(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	var keep []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "Cloning into") {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) == 0 {
		return "unknown error"
	}
	msg := strings.Join(keep, "; ")
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return msg
}

// ValidHTTPURL reports whether url looks like an http(s) git remote.
func ValidHTTPURL(url string) bool {
	return strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")
}
