package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsReleaseAndNewer(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.3.0": true, "v1.2.3-rc.1": true, "dev": false, "1da7b22": false,
		"v0.3.0-dirty": false, "v0.3.0-3-gabcdef": true, "0.3.0": false,
	} {
		if got := IsRelease(v); got != want {
			t.Errorf("IsRelease(%q) = %v, want %v", v, got, want)
		}
	}
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.4.0", "v0.3.9", true},
		{"v0.3.10", "v0.3.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.3.0", "v0.3.0", false},
		{"v0.3.0", "v0.4.0", false},
		{"v0.3.0", "v0.3.0-rc.1", true},
		{"v0.3.0-rc.1", "v0.3.0", false},
		{"v0.3.0", "dev", true},
		{"dev", "v0.3.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// releaseServer serves a fake GitHub: the latest redirect, one archive, and
// its checksums.
func releaseServer(t *testing.T, tag string, bin []byte) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		data []byte
	}{{"README.md", []byte("hi")}, {"keel", bin}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		tw.Write(f.data)
	}
	tw.Close()
	gz.Close()
	archive := fmt.Sprintf("keel_%s_linux_amd64.tar.gz", tag[1:])
	sum := sha256.Sum256(buf.Bytes())
	sums := fmt.Sprintf("%s  keel_%s_windows_amd64.zip\n%s  %s\n", "00", tag[1:], hex.EncodeToString(sum[:]), archive)

	mux := http.NewServeMux()
	mux.HandleFunc("/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+Repo+"/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/"+archive, func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	})
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestTagAndUpdate(t *testing.T) {
	srv := releaseServer(t, "v9.9.9", []byte("#!/bin/sh\necho new\n"))
	tag, err := LatestTag(context.Background(), srv.URL)
	if err != nil || tag != "v9.9.9" {
		t.Fatalf("LatestTag = %q, %v", tag, err)
	}

	dir := t.TempDir()
	exe := filepath.Join(dir, "keel")
	os.WriteFile(exe, []byte("old"), 0o755)
	var logs []string
	res, err := Update(context.Background(), Options{
		BaseURL: srv.URL, Executable: exe, OS: "linux", Arch: "amd64",
		Log: func(s string) { logs = append(logs, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "v9.9.9" || res.Executable != exe {
		t.Errorf("result = %+v", res)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "#!/bin/sh\necho new\n" {
		t.Errorf("binary not replaced: %q", got)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm()&0o100 == 0 {
		t.Error("binary not executable")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
	if len(logs) != 2 {
		t.Errorf("logs = %v", logs)
	}

	// A tampered archive is rejected before anything is written.
	bad := releaseServer(t, "v9.9.9", []byte("evil"))
	badMux := http.NewServeMux()
	badMux.Handle("/", bad.Config.Handler)
	tampered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "checksums.txt" {
			w.Write([]byte("deadbeef  keel_9.9.9_linux_amd64.tar.gz\n"))
			return
		}
		badMux.ServeHTTP(w, r)
	}))
	defer tampered.Close()
	os.WriteFile(exe, []byte("old"), 0o755)
	if _, err := Update(context.Background(), Options{BaseURL: tampered.URL, Executable: exe, OS: "linux", Arch: "amd64"}); err == nil {
		t.Fatal("expected checksum mismatch")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Error("binary replaced despite checksum mismatch")
	}
}

func TestBackgroundCheck(t *testing.T) {
	srv := releaseServer(t, "v2.0.0", []byte("x"))
	state := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Now()

	// First run: nothing cached, the check fetches in the background.
	c := start("v1.0.0", state, srv.URL, now)
	<-c.done
	if got := c.Notice(0); got == "" {
		t.Fatal("expected a notice after the fetch completed")
	}
	if st := ReadState(state); st.Latest != "v2.0.0" || st.CheckedAt.IsZero() {
		t.Errorf("state not saved: %+v", st)
	}

	// Within the interval the saved answer is used — no server needed.
	srv.Close()
	c = start("v1.0.0", state, srv.URL, now.Add(time.Hour))
	select {
	case <-c.done:
	default:
		t.Fatal("cached check should be answered immediately")
	}
	if c.Notice(0) == "" {
		t.Error("expected notice from cache")
	}
	if c = start("v2.0.0", state, srv.URL, now.Add(time.Hour)); c.Notice(0) != "" {
		t.Error("no notice when already on the latest")
	}

	// A stale cache with an unreachable server falls back to the last answer.
	c = start("v1.0.0", state, srv.URL, now.Add(48*time.Hour))
	<-c.done
	if c.Notice(0) == "" {
		t.Error("expected fallback to the previous answer")
	}

	if Start("dev") != nil {
		t.Error("dev builds must not check")
	}
	t.Setenv(DisableEnv, "1")
	if Start("v1.0.0") != nil {
		t.Error("KEEL_NO_UPDATE_CHECK must disable the check")
	}
}
