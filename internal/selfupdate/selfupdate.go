// Package selfupdate finds and installs newer releases of the keel CLI.
//
// Releases are published on GitHub by GoReleaser (see .goreleaser.yaml):
// one archive per platform named keel_<version>_<os>_<arch>.tar.gz (.zip on
// Windows) plus a checksums.txt. This package mirrors what
// scripts/install.sh does — resolve the latest tag, download the archive,
// verify its SHA-256, extract the binary — and then swaps the running
// executable for the new one.
//
// It also provides the throttled background check the CLI runs on every
// command, so users hear about a new release at the cost of one short,
// bounded wait per day.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases are published from.
const Repo = "UcGeorge/keel"

// DefaultBaseURL is the GitHub host releases are served from.
const DefaultBaseURL = "https://github.com"

// CheckInterval is how often the background check contacts GitHub.
const CheckInterval = 24 * time.Hour

// DisableEnv, when set to any value, turns the background check off.
const DisableEnv = "KEEL_NO_UPDATE_CHECK"

var releaseRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// IsRelease reports whether v looks like a release tag (v1.2.3, optionally
// with a pre-release suffix). Development builds carry "dev", a commit
// hash, or a "-dirty" suffix and are never offered updates automatically.
func IsRelease(v string) bool {
	if strings.HasSuffix(v, "-dirty") {
		return false
	}
	return releaseRe.MatchString(v)
}

// Newer reports whether release tag a is newer than release tag b. Either
// side that is not a release tag compares as older than any release.
func Newer(a, b string) bool {
	return compare(a, b) > 0
}

func compare(a, b string) int {
	ma, oka := parse(a)
	mb, okb := parse(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := 0; i < 3; i++ {
		if ma.nums[i] != mb.nums[i] {
			if ma.nums[i] > mb.nums[i] {
				return 1
			}
			return -1
		}
	}
	// Same numbers: a final release outranks a pre-release; two
	// pre-releases compare as strings.
	switch {
	case ma.pre == mb.pre:
		return 0
	case ma.pre == "":
		return 1
	case mb.pre == "":
		return -1
	case ma.pre > mb.pre:
		return 1
	default:
		return -1
	}
}

type semver struct {
	nums [3]int
	pre  string
}

func parse(v string) (semver, bool) {
	m := releaseRe.FindStringSubmatch(v)
	if m == nil {
		return semver{}, false
	}
	var s semver
	for i := 0; i < 3; i++ {
		s.nums[i], _ = strconv.Atoi(m[i+1])
	}
	s.pre = m[4]
	return s, true
}

// LatestTag resolves the newest release tag by following the redirect of
// the releases/latest page — no API token, no rate limit.
func LatestTag(ctx context.Context, baseURL string) (string, error) {
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, baseURL+"/"+Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "keel-cli")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	_, tag, ok := strings.Cut(loc, "/releases/tag/")
	if resp.StatusCode/100 != 3 || !ok || tag == "" {
		return "", fmt.Errorf("could not determine the latest release of %s (HTTP %d)", Repo, resp.StatusCode)
	}
	return strings.TrimSpace(tag), nil
}

// State is what the background check remembers between runs.
type State struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// StatePath is the file the check state is kept in, next to dev.key.
func StatePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "keel", "update-check.json")
}

// ReadState loads the saved state; a missing or unreadable file yields the
// zero State.
func ReadState(path string) State {
	var s State
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s)
	return s
}

// WriteState saves the state, creating the directory when needed.
func WriteState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Check is a background update check in flight or already answered.
type Check struct {
	current string
	done    chan struct{}
	latest  string // valid once done is closed
	cached  string // what the previous check found, if anything
}

// Start begins a background check for a newer release than current, or
// returns nil when the check does not apply: a development build, the
// KEEL_NO_UPDATE_CHECK or CI environment variables set. Within
// CheckInterval of the last check the saved answer is used and nothing is
// fetched. Call Notice when the command is finished.
func Start(current string) *Check {
	if !IsRelease(current) || os.Getenv(DisableEnv) != "" || os.Getenv("CI") != "" {
		return nil
	}
	return start(current, StatePath(), DefaultBaseURL, time.Now())
}

func start(current, statePath, baseURL string, now time.Time) *Check {
	st := ReadState(statePath)
	c := &Check{current: current, done: make(chan struct{}), cached: st.Latest}
	if now.Sub(st.CheckedAt) < CheckInterval {
		c.latest = st.Latest
		close(c.done)
		return c
	}
	go func() {
		defer close(c.done)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tag, err := LatestTag(ctx, baseURL)
		if err != nil {
			// Keep the previous answer, but count this as today's attempt
			// so an offline machine does not retry on every command.
			c.latest = st.Latest
			_ = WriteState(statePath, State{CheckedAt: now, Latest: st.Latest})
			return
		}
		c.latest = tag
		_ = WriteState(statePath, State{CheckedAt: now, Latest: tag})
	}()
	return c
}

// Notice returns the message to show when a newer release is known, or
// "". A check still in flight is given at most wait to finish — commands
// often end before a network round trip does — after which the previous
// answer is used and the result is picked up by the next command.
func (c *Check) Notice(wait time.Duration) string {
	if c == nil {
		return ""
	}
	latest := c.cached
	select {
	case <-c.done:
		latest = c.latest
	default:
		// Still in flight: grant it the bounded wait. (A single select
		// over done and a zero timer would pick between two ready cases
		// at random — hence the explicit check above.)
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-c.done:
			latest = c.latest
		case <-timer.C:
		}
	}
	if latest == "" || !Newer(latest, c.current) {
		return ""
	}
	return fmt.Sprintf("A new release of keel is available: %s → %s\nRun `keel update` to install it.", c.current, latest)
}

// Options control Update.
type Options struct {
	// Version is the release tag to install; empty means the latest.
	Version string
	// BaseURL overrides the GitHub host (tests).
	BaseURL string
	// Executable is the binary to replace; empty means the running one.
	Executable string
	// OS and Arch select the archive; empty means the running platform.
	OS, Arch string
	// Log receives progress lines; nil discards them.
	Log func(string)
}

// Result describes a completed update.
type Result struct {
	Version    string
	Executable string
	Archive    string
}

// Update downloads the release, verifies it, and replaces the executable.
func Update(ctx context.Context, opts Options) (*Result, error) {
	log := opts.Log
	if log == nil {
		log = func(string) {}
	}
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	goos, arch := opts.OS, opts.Arch
	if goos == "" {
		goos = runtime.GOOS
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	version := opts.Version
	if version == "" {
		var err error
		if version, err = LatestTag(ctx, base); err != nil {
			return nil, err
		}
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	exe := opts.Executable
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, err
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
	}

	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	archive := fmt.Sprintf("keel_%s_%s_%s%s", strings.TrimPrefix(version, "v"), goos, arch, ext)
	dl := fmt.Sprintf("%s/%s/releases/download/%s/", base, Repo, version)

	log(fmt.Sprintf("Downloading %s", archive))
	data, err := fetch(ctx, dl+archive)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", archive, err)
	}
	sums, err := fetch(ctx, dl+"checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("download checksums.txt: %w", err)
	}
	if err := verify(data, sums, archive); err != nil {
		return nil, err
	}
	log("Verified SHA-256 against checksums.txt")

	binName := "keel"
	if goos == "windows" {
		binName = "keel.exe"
	}
	bin, err := extract(data, ext, binName)
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w", archive, err)
	}
	if err := replaceExecutable(exe, bin, goos); err != nil {
		return nil, err
	}
	return &Result{Version: version, Executable: exe, Archive: archive}, nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "keel-cli")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// verify checks data against the "<sha256>  <name>" line for name in a
// GoReleaser checksums file.
func verify(data, sums []byte, name string) error {
	expected := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			expected = fields[0]
		}
	}
	if expected == "" {
		return fmt.Errorf("no checksum for %s in checksums.txt", name)
	}
	sum := sha256.Sum256(data)
	if actual := hex.EncodeToString(sum[:]); actual != expected {
		return fmt.Errorf("checksum mismatch for %s (expected %s, got %s)", name, expected, actual)
	}
	return nil
}

// extract returns the named file from a .tar.gz or .zip archive.
func extract(data []byte, ext, name string) ([]byte, error) {
	if ext == ".zip" {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == name {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("%s not found in archive", name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s not found in archive", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return io.ReadAll(tr)
		}
	}
}

// replaceExecutable writes bin next to exe and moves it into place. On
// Unix the rename is atomic and works while the old binary runs; Windows
// cannot overwrite a running executable, so the old one is renamed aside
// first.
func replaceExecutable(exe string, bin []byte, goos string) error {
	mode := os.FileMode(0o755)
	if info, err := os.Stat(exe); err == nil {
		mode = info.Mode().Perm() | 0o100
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".keel-update-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot write to %s: %w\nRe-run with elevated permissions (e.g. `sudo keel update`) or reinstall with the install script", dir, err)
		}
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return err
	}
	if goos == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanup()
			return err
		}
		if err := os.Rename(tmpName, exe); err != nil {
			_ = os.Rename(old, exe)
			cleanup()
			return err
		}
		_ = os.Remove(old) // may fail while the old binary is still running; harmless
		return nil
	}
	if err := os.Rename(tmpName, exe); err != nil {
		cleanup()
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot replace %s: %w\nRe-run with elevated permissions (e.g. `sudo keel update`) or reinstall with the install script", exe, err)
		}
		return err
	}
	return nil
}
