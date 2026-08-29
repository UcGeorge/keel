// Package engine executes deployments.
//
// A run has two phases: building the environment image from the deployment's
// Dockerfile, then executing the steps inside a container of that image with
// the repository mounted at /workspace and every variable exported as an
// environment variable. Docker must be available on the host.
package engine

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Phase is the coarse stage a run is in.
type Phase string

const (
	PhaseBuilding Phase = "building"
	PhaseRunning  Phase = "running"
)

// StepStatus is the state of a single step.
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

// Sink receives run output. Implementations must tolerate concurrent runs
// but calls for a single run are always serialized.
type Sink interface {
	// Log receives one output line (secrets already masked).
	Log(line string)
	// Phase reports a phase transition.
	Phase(p Phase)
	// StepStatus reports a step (0-based) entering a new state.
	StepStatus(idx int, status StepStatus)
}

// Step is one command to execute, resolved from the deployment config.
type Step struct {
	Name string
	Run  string
}

// Spec describes one run.
type Spec struct {
	// RunID uniquely identifies the run; it names the container.
	RunID string
	// Deployment is the deployment name (metadata only).
	Deployment string
	// Target is the deployment target name (metadata only).
	Target string
	// TargetID is the target's stable identifier (metadata only). Targets
	// can be renamed, so steps that must key persistent state — a Terraform
	// state key, a stack name — read this rather than Target. Empty when
	// there is no stored target (keel deploy without --target).
	TargetID string
	// RepoDir is the absolute path of the repository checkout. It is
	// mounted read-write at /workspace and used as the build context root.
	RepoDir string
	// Dockerfile is the environment Dockerfile path, relative to RepoDir.
	Dockerfile string
	// Context is the Docker build context, relative to RepoDir.
	Context string
	// Steps are executed in order.
	Steps []Step
	// Env is the resolved variable map exported into the run.
	Env map[string]string
	// SecretValues are masked in every log line.
	SecretValues []string
	// Outputs are the names of environment variables to capture from the
	// container at the end of a fully successful run.
	Outputs []string
	// ImageTag names the built image. A stable tag per target keeps Docker
	// layer caching effective across runs. Defaults to "keel/run-<runid>".
	ImageTag string
}

// Result is the outcome of a run.
type Result struct {
	// ExitCode is the container exit code; -1 if the container never ran.
	ExitCode int
	// FailedStep is the 0-based index of the step that failed, or -1.
	FailedStep int
	// Canceled reports whether the run was canceled via context.
	Canceled bool
	// Outputs holds the captured output values by name. Only variables that
	// were set at the end of a fully successful run appear; their values
	// never pass through the visible log stream.
	Outputs map[string]string
}

// Runner executes runs with the Docker CLI.
type Runner struct {
	// Docker is the docker binary. Defaults to "docker".
	Docker string
}

// CheckDocker verifies the Docker daemon is reachable.
func (r *Runner) CheckDocker(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, r.docker(), "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker is not available (is Docker running?): %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *Runner) docker() string {
	if r.Docker != "" {
		return r.Docker
	}
	return "docker"
}

// containerName returns the container name for a run ID.
func containerName(runID string) string { return "keel-run-" + runID }

// Cancel force-removes the container of a run, interrupting it if running.
// It is safe to call for runs that never started a container.
func (r *Runner) Cancel(runID string) {
	_ = exec.Command(r.docker(), "rm", "-f", containerName(runID)).Run()
}

// Run executes the spec. Output flows to sink; the returned error is non-nil
// for infrastructure failures and step failures alike (Result carries the
// detail). Cancel the context to abort the run.
func (r *Runner) Run(ctx context.Context, spec Spec, sink Sink) (Result, error) {
	res := Result{ExitCode: -1, FailedStep: -1}
	ms := &maskingSink{sink: sink, secrets: nonEmpty(spec.SecretValues)}

	for i := range spec.Steps {
		ms.StepStatus(i, StepPending)
	}

	imageTag := spec.ImageTag
	if imageTag == "" {
		imageTag = "keel/run-" + strings.ToLower(spec.RunID)
	}

	// Phase 1: build the environment image.
	ms.Phase(PhaseBuilding)
	ms.Log(fmt.Sprintf("=> Building environment image from %s", spec.Dockerfile))
	buildArgs := []string{
		"build",
		"--file", joinRepo(spec.RepoDir, spec.Dockerfile),
		"--tag", imageTag,
		joinRepo(spec.RepoDir, spec.Context),
	}
	if err := r.stream(ctx, ms, nil, buildArgs...); err != nil {
		skipAll(ms, spec.Steps, 0)
		if ctx.Err() != nil {
			res.Canceled = true
			return res, context.Canceled
		}
		return res, fmt.Errorf("environment image build failed: %w", err)
	}
	ms.Log("=> Environment image ready")

	// Phase 2: run the steps.
	ms.Phase(PhaseRunning)
	script := BuildScript(spec.Steps, spec.Outputs)

	runArgs := []string{
		"run", "--rm",
		"--name", containerName(spec.RunID),
		"--volume", spec.RepoDir + ":/workspace",
		"--workdir", "/workspace",
		"--env", "KEEL_DEPLOYMENT=" + spec.Deployment,
		"--env", "KEEL_TARGET=" + spec.Target,
		"--env", "KEEL_TARGET_ID=" + spec.TargetID,
		"--env", "KEEL_RUN_ID=" + spec.RunID,
	}
	env := os.Environ()
	for name, value := range spec.Env {
		// Values travel via the process environment, not argv, so they are
		// never visible in the host process list.
		runArgs = append(runArgs, "--env", name)
		env = append(env, name+"="+value)
	}
	runArgs = append(runArgs, imageTag, "/bin/sh", "-c", script)

	tracker := &stepTracker{sink: ms, steps: spec.Steps, wantOutputs: stringSet(spec.Outputs)}
	err := r.streamEnv(ctx, tracker, env, runArgs...)

	// Belt and braces: --rm removes the container, but a killed docker CLI
	// (cancellation) can leave it behind.
	if ctx.Err() != nil {
		r.Cancel(spec.RunID)
	}

	res.FailedStep = tracker.finish(err == nil)
	if err == nil {
		res.ExitCode = 0
		res.Outputs = tracker.outputs
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
	}
	if ctx.Err() != nil {
		res.Canceled = true
		return res, context.Canceled
	}
	if res.FailedStep >= 0 {
		return res, fmt.Errorf("step %q failed with exit code %d", spec.Steps[res.FailedStep].Name, res.ExitCode)
	}
	return res, fmt.Errorf("run failed: %w", err)
}

// stepMarker matches the marker lines emitted by BuildScript.
var stepMarker = regexp.MustCompile(`^##KEEL:STEP:(\d+):(BEGIN|OK)##$`)

// outputMarker matches the output capture lines emitted by BuildScript:
// ##KEEL:OUTPUT##NAME##<hex-encoded value>##. Values travel hex-encoded so
// any byte content survives the line-oriented log stream, and the tracker
// consumes these lines before they reach the visible log.
var outputMarker = regexp.MustCompile(`^##KEEL:OUTPUT##([A-Za-z_][A-Za-z0-9_]*)##([0-9a-fA-F]*)##$`)

// envSnapshot is the in-container file each step's exported environment is
// persisted to, so `export FOO=…` in one step is visible to later steps and
// to output capture. /tmp is container-local and gone when the run ends.
const envSnapshot = "/tmp/.keel-env"

// maxOutputBytes caps a single captured output value. Larger values are
// skipped with a log note (hex encoding doubles the bytes on the wire, and
// the log scanner has a 1MB line limit).
const maxOutputBytes = 64 * 1024

// BuildScript assembles the shell script that executes the steps with
// per-step markers. Each step body runs in a subshell so its shell options
// and working-directory changes stay contained — but its exported
// environment is snapshotted (even on failure, via the EXIT trap) and
// restored at the start of the next step, so `export` carries forward.
// `set -e` at the top aborts the script the moment a step's subshell exits
// non-zero. When every step succeeded, the declared output variables are
// read from the final environment and emitted as consumed marker lines.
func BuildScript(steps []Step, outputs []string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for i, s := range steps {
		fmt.Fprintf(&b, "echo '##KEEL:STEP:%d:BEGIN##'\n", i)
		fmt.Fprintf(&b, "(\ntrap 'export -p > %s 2>/dev/null' EXIT\nif [ -f %s ]; then . %s 2>/dev/null || true; fi\n%s\n)\n", envSnapshot, envSnapshot, envSnapshot, s.Run)
		fmt.Fprintf(&b, "echo '##KEEL:STEP:%d:OK##'\n", i)
	}
	if len(outputs) > 0 {
		b.WriteString("(\n")
		fmt.Fprintf(&b, "if [ -f %s ]; then . %s 2>/dev/null || true; fi\n", envSnapshot, envSnapshot)
		for _, name := range outputs {
			fmt.Fprintf(&b, "if [ -n \"${%s+x}\" ]; then\n", name)
			fmt.Fprintf(&b, "  if [ \"$(printf '%%s' \"$%s\" | wc -c)\" -gt %d ]; then\n", name, maxOutputBytes)
			fmt.Fprintf(&b, "    echo '[keel] output %s exceeds %dKB and was not captured'\n", name, maxOutputBytes/1024)
			b.WriteString("  else\n")
			fmt.Fprintf(&b, "    printf '##KEEL:OUTPUT##%s##%%s##\\n' \"$(printf '%%s' \"$%s\" | od -An -v -tx1 | tr -d ' \\n')\"\n", name, name)
			b.WriteString("  fi\n")
			b.WriteString("fi\n")
		}
		b.WriteString(")\n")
	}
	return b.String()
}

// stepTracker consumes run output, translating marker lines into step
// status events, capturing output values, and passing everything else
// through.
type stepTracker struct {
	sink        Sink
	steps       []Step
	wantOutputs map[string]bool
	mu          sync.Mutex
	current     int // index of the step currently running; -1 before the first
	started     bool
	done        map[int]bool
	outputs     map[string]string
}

func (t *stepTracker) Phase(p Phase)                  { t.sink.Phase(p) }
func (t *stepTracker) StepStatus(i int, s StepStatus) { t.sink.StepStatus(i, s) }
func (t *stepTracker) Log(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m := outputMarker.FindStringSubmatch(line); m != nil {
		// Consume output lines — they never reach the visible log.
		name := m[1]
		if !t.wantOutputs[name] {
			return
		}
		value, err := hex.DecodeString(m[2])
		if err != nil {
			t.sink.Log("[keel] output " + name + " could not be decoded")
			return
		}
		if t.outputs == nil {
			t.outputs = map[string]string{}
		}
		t.outputs[name] = string(value)
		return
	}
	if m := stepMarker.FindStringSubmatch(line); m != nil {
		idx, _ := strconv.Atoi(m[1])
		if idx < 0 || idx >= len(t.steps) {
			return
		}
		if t.done == nil {
			t.done = map[int]bool{}
		}
		switch m[2] {
		case "BEGIN":
			t.current = idx
			t.started = true
			t.sink.StepStatus(idx, StepRunning)
			t.sink.Log(fmt.Sprintf("=> Step %d/%d: %s", idx+1, len(t.steps), t.steps[idx].Name))
		case "OK":
			t.done[idx] = true
			t.sink.StepStatus(idx, StepSucceeded)
		}
		return
	}
	t.sink.Log(line)
}

// finish settles final step states once the container exits. It returns the
// index of the failed step, or -1.
func (t *stepTracker) finish(succeeded bool) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if succeeded {
		return -1
	}
	failed := -1
	if t.started && !t.done[t.current] {
		failed = t.current
		t.sink.StepStatus(t.current, StepFailed)
	}
	from := t.current + 1
	if !t.started {
		from = 0
	}
	for i := from; i < len(t.steps); i++ {
		if !t.done[i] {
			t.sink.StepStatus(i, StepSkipped)
		}
	}
	return failed
}

func skipAll(sink Sink, steps []Step, from int) {
	for i := from; i < len(steps); i++ {
		sink.StepStatus(i, StepSkipped)
	}
}

// stream runs the docker CLI, forwarding each output line to the sink.
func (r *Runner) stream(ctx context.Context, sink Sink, env []string, args ...string) error {
	return r.streamEnv(ctx, sink, env, args...)
}

func (r *Runner) streamEnv(ctx context.Context, sink Sink, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, r.docker(), args...)
	if env != nil {
		cmd.Env = env
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	forward := func(rd io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(rd)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSuffix(sc.Text(), "\r")
			// docker build progress uses carriage returns; keep the final
			// segment of such lines.
			if i := strings.LastIndex(line, "\r"); i >= 0 {
				line = line[i+1:]
			}
			sink.Log(line)
		}
		if err := sc.Err(); err != nil {
			// A pathological line overflowed the buffer; keep draining so
			// the child never blocks on a full pipe.
			sink.Log("[keel] log line dropped: " + err.Error())
			_, _ = io.Copy(io.Discard, rd)
		}
	}
	wg.Add(2)
	go forward(stdout)
	go forward(stderr)
	wg.Wait()
	return cmd.Wait()
}

// maskingSink replaces secret values in log lines and serializes all sink
// calls behind one mutex.
type maskingSink struct {
	mu      sync.Mutex
	sink    Sink
	secrets []string
}

const mask = "•••"

func (m *maskingSink) Log(line string) {
	for _, s := range m.secrets {
		line = strings.ReplaceAll(line, s, mask)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink.Log(line)
}

func (m *maskingSink) Phase(p Phase) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink.Phase(p)
}

func (m *maskingSink) StepStatus(i int, s StepStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink.StepStatus(i, s)
}

func stringSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

func nonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

func joinRepo(repoDir, rel string) string {
	if rel == "" || rel == "." {
		return repoDir
	}
	return repoDir + string(os.PathSeparator) + rel
}
