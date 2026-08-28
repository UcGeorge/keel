package engine

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// recorder is a Sink capturing everything for assertions.
type recorder struct {
	mu     sync.Mutex
	lines  []string
	phases []Phase
	steps  map[int][]StepStatus
}

func newRecorder() *recorder { return &recorder{steps: map[int][]StepStatus{}} }

func (r *recorder) Log(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}
func (r *recorder) Phase(p Phase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phases = append(r.phases, p)
}
func (r *recorder) StepStatus(i int, s StepStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps[i] = append(r.steps[i], s)
}
func (r *recorder) last(i int) StepStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	ss := r.steps[i]
	if len(ss) == 0 {
		return ""
	}
	return ss[len(ss)-1]
}
func (r *recorder) allLines() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

func TestBuildScript(t *testing.T) {
	script := BuildScript([]Step{
		{Name: "One", Run: "echo hello"},
		{Name: "Two", Run: "echo a\necho b"},
	}, []string{"SERVICE_URL"})
	for _, want := range []string{
		"set -e",
		"##KEEL:STEP:0:BEGIN##",
		"echo hello",
		"##KEEL:STEP:0:OK##",
		"##KEEL:STEP:1:BEGIN##",
		"##KEEL:STEP:1:OK##",
		// Env persists across step subshells via the snapshot file…
		"trap 'export -p > /tmp/.keel-env",
		// …and declared outputs are emitted hex-encoded at the end.
		`if [ -n "${SERVICE_URL+x}" ]`,
		"##KEEL:OUTPUT##SERVICE_URL##",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	if plain := BuildScript([]Step{{Name: "One", Run: "true"}}, nil); strings.Contains(plain, "KEEL:OUTPUT") {
		t.Error("script without outputs must not emit output markers")
	}
}

func TestStepTrackerOutputs(t *testing.T) {
	rec := newRecorder()
	tr := &stepTracker{sink: rec, steps: []Step{{Name: "a"}}, current: -1,
		wantOutputs: map[string]bool{"URL": true, "EMPTY": true}}
	tr.Log("##KEEL:STEP:0:BEGIN##")
	tr.Log("##KEEL:STEP:0:OK##")
	tr.Log("##KEEL:OUTPUT##URL##" + hex.EncodeToString([]byte("https://x\nline2")) + "##")
	tr.Log("##KEEL:OUTPUT##EMPTY####")
	tr.Log("##KEEL:OUTPUT##UNDECLARED##" + hex.EncodeToString([]byte("nope")) + "##")
	if tr.outputs["URL"] != "https://x\nline2" {
		t.Errorf("URL = %q", tr.outputs["URL"])
	}
	if v, ok := tr.outputs["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY = %q (present %v), want empty string", v, ok)
	}
	if _, ok := tr.outputs["UNDECLARED"]; ok {
		t.Error("undeclared output captured")
	}
	if out := rec.allLines(); strings.Contains(out, "KEEL:OUTPUT") || strings.Contains(out, "https://x") {
		t.Errorf("output value leaked into logs:\n%s", out)
	}
}

func TestStepTrackerSuccess(t *testing.T) {
	rec := newRecorder()
	tr := &stepTracker{sink: rec, steps: []Step{{Name: "a"}, {Name: "b"}}, current: -1}
	tr.Log("##KEEL:STEP:0:BEGIN##")
	tr.Log("doing a")
	tr.Log("##KEEL:STEP:0:OK##")
	tr.Log("##KEEL:STEP:1:BEGIN##")
	tr.Log("##KEEL:STEP:1:OK##")
	if failed := tr.finish(true); failed != -1 {
		t.Errorf("failed = %d", failed)
	}
	if rec.last(0) != StepSucceeded || rec.last(1) != StepSucceeded {
		t.Errorf("statuses = %v", rec.steps)
	}
	out := rec.allLines()
	if strings.Contains(out, "##KEEL") {
		t.Errorf("marker leaked into logs:\n%s", out)
	}
	if !strings.Contains(out, "Step 1/2: a") || !strings.Contains(out, "doing a") {
		t.Errorf("logs = %s", out)
	}
}

func TestStepTrackerFailure(t *testing.T) {
	rec := newRecorder()
	tr := &stepTracker{sink: rec, steps: []Step{{Name: "a"}, {Name: "b"}, {Name: "c"}}, current: -1}
	tr.Log("##KEEL:STEP:0:BEGIN##")
	tr.Log("##KEEL:STEP:0:OK##")
	tr.Log("##KEEL:STEP:1:BEGIN##")
	tr.Log("boom")
	if failed := tr.finish(false); failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if rec.last(0) != StepSucceeded || rec.last(1) != StepFailed || rec.last(2) != StepSkipped {
		t.Errorf("statuses = %v", rec.steps)
	}
}

func TestStepTrackerFailureBeforeAnyStep(t *testing.T) {
	rec := newRecorder()
	tr := &stepTracker{sink: rec, steps: []Step{{Name: "a"}}, current: -1}
	if failed := tr.finish(false); failed != -1 {
		t.Errorf("failed = %d", failed)
	}
	if rec.last(0) != StepSkipped {
		t.Errorf("statuses = %v", rec.steps)
	}
}

func TestMaskingSink(t *testing.T) {
	rec := newRecorder()
	ms := &maskingSink{sink: rec, secrets: []string{"hunter2", "AKIA123"}}
	ms.Log("password is hunter2 and key is AKIA123 ok")
	out := rec.allLines()
	if strings.Contains(out, "hunter2") || strings.Contains(out, "AKIA123") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, mask) {
		t.Fatalf("mask missing: %s", out)
	}
}

func dockerAvailable() bool {
	if os.Getenv("KEEL_TEST_SKIP_DOCKER") != "" {
		return false
	}
	return exec.Command("docker", "version").Run() == nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const testDockerfile = "FROM alpine:3.20\nWORKDIR /workspace\n"

func TestRunIntegration(t *testing.T) {
	if testing.Short() || !dockerAvailable() {
		t.Skip("docker not available")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "deploy", "Dockerfile"), testDockerfile)
	writeFile(t, filepath.Join(dir, "hello.txt"), "from the repo\n")

	rec := newRecorder()
	r := &Runner{}
	res, err := r.Run(context.Background(), Spec{
		RunID:      "test-ok",
		Deployment: "prod",
		Target:     "t1",
		RepoDir:    dir,
		Dockerfile: "deploy/Dockerfile",
		Context:    ".",
		Steps: []Step{
			{Name: "Greet", Run: "echo running as $KEEL_DEPLOYMENT/$KEEL_TARGET with GREETING=$GREETING"},
			{Name: "Read repo file", Run: "cat hello.txt"},
			{Name: "Use secret", Run: "echo the secret is $TOKEN"},
		},
		Env:          map[string]string{"GREETING": "ahoy", "TOKEN": "supersecret99"},
		SecretValues: []string{"supersecret99"},
	}, rec)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, rec.allLines())
	}
	if res.ExitCode != 0 || res.FailedStep != -1 {
		t.Errorf("result = %+v", res)
	}
	out := rec.allLines()
	if !strings.Contains(out, "running as prod/t1 with GREETING=ahoy") {
		t.Errorf("env not injected:\n%s", out)
	}
	if !strings.Contains(out, "from the repo") {
		t.Errorf("workspace mount missing:\n%s", out)
	}
	if strings.Contains(out, "supersecret99") {
		t.Errorf("secret leaked:\n%s", out)
	}
	for i := 0; i < 3; i++ {
		if rec.last(i) != StepSucceeded {
			t.Errorf("step %d = %s", i, rec.last(i))
		}
	}
}

func TestRunIntegrationOutputs(t *testing.T) {
	if testing.Short() || !dockerAvailable() {
		t.Skip("docker not available")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "deploy", "Dockerfile"), testDockerfile)

	rec := newRecorder()
	r := &Runner{}
	res, err := r.Run(context.Background(), Spec{
		RunID:      "test-outputs",
		Deployment: "prod",
		Target:     "t1",
		RepoDir:    dir,
		Dockerfile: "deploy/Dockerfile",
		Context:    ".",
		Steps: []Step{
			// Exports must survive into later steps and into output capture.
			{Name: "Provision", Run: "export SERVICE_URL=https://api.example.com\nexport DB_PASSWORD=hunter2secret\nexport EMPTY_OUT="},
			{Name: "Check", Run: "echo url is $SERVICE_URL"},
		},
		Env:     map[string]string{"GREETING": "ahoy"},
		Outputs: []string{"SERVICE_URL", "DB_PASSWORD", "EMPTY_OUT", "NEVER_SET", "GREETING"},
	}, rec)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, rec.allLines())
	}
	want := map[string]string{
		"SERVICE_URL": "https://api.example.com",
		"DB_PASSWORD": "hunter2secret",
		"EMPTY_OUT":   "",
		"GREETING":    "ahoy", // input variables are outputs too
	}
	for name, val := range want {
		got, ok := res.Outputs[name]
		if !ok || got != val {
			t.Errorf("output %s = %q (present %v), want %q", name, got, ok, val)
		}
	}
	if _, ok := res.Outputs["NEVER_SET"]; ok {
		t.Error("NEVER_SET should be absent")
	}
	out := rec.allLines()
	if !strings.Contains(out, "url is https://api.example.com") {
		t.Errorf("export did not survive into the next step:\n%s", out)
	}
	if strings.Contains(out, "hunter2secret") || strings.Contains(out, "KEEL:OUTPUT") {
		t.Errorf("output capture leaked into the log:\n%s", out)
	}
}

func TestRunIntegrationFailure(t *testing.T) {
	if testing.Short() || !dockerAvailable() {
		t.Skip("docker not available")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "deploy", "Dockerfile"), testDockerfile)

	rec := newRecorder()
	r := &Runner{}
	res, err := r.Run(context.Background(), Spec{
		RunID:      "test-fail",
		RepoDir:    dir,
		Dockerfile: "deploy/Dockerfile",
		Context:    ".",
		Steps: []Step{
			{Name: "OK", Run: "echo fine"},
			{Name: "Boom", Run: "echo about to fail; exit 3"},
			{Name: "Never", Run: "echo unreachable"},
		},
	}, rec)
	if err == nil {
		t.Fatal("expected error")
	}
	if res.FailedStep != 1 || res.ExitCode != 3 {
		t.Errorf("result = %+v", res)
	}
	if rec.last(0) != StepSucceeded || rec.last(1) != StepFailed || rec.last(2) != StepSkipped {
		t.Errorf("statuses = %v", rec.steps)
	}
	if strings.Contains(rec.allLines(), "unreachable") {
		t.Error("step after failure still ran")
	}
}

func TestRunIntegrationBuildFailure(t *testing.T) {
	if testing.Short() || !dockerAvailable() {
		t.Skip("docker not available")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "deploy", "Dockerfile"), "FROM alpine:3.20\nRUN false\n")

	rec := newRecorder()
	r := &Runner{}
	res, err := r.Run(context.Background(), Spec{
		RunID:      "test-buildfail",
		RepoDir:    dir,
		Dockerfile: "deploy/Dockerfile",
		Context:    ".",
		Steps:      []Step{{Name: "Never", Run: "echo no"}},
	}, rec)
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("err = %v", err)
	}
	if res.FailedStep != -1 {
		t.Errorf("result = %+v", res)
	}
	if rec.last(0) != StepSkipped {
		t.Errorf("statuses = %v", rec.steps)
	}
}
