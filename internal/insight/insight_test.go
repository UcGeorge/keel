package insight

import (
	"fmt"
	"strings"
	"testing"
)

func lines(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s %d", prefix, i)
	}
	return out
}

func TestTrimLogKeepsHeadAndFailedStep(t *testing.T) {
	var log []string
	log = append(log, "=> Cloning repo", "=> Checked out abc", "=> Building environment image from deploy/Dockerfile")
	log = append(log, lines(200, "build")...)
	log = append(log, "=> Environment image ready", "=> Step 1/3: Auth")
	log = append(log, lines(300, "auth")...)
	log = append(log, "=> Step 2/3: Deploy")
	log = append(log, lines(50, "deploy")...)
	log = append(log, "error: terraform apply failed")
	failed := 1
	out := TrimLog(log, &failed, MaxLogChars)
	if out[0] != "=> Cloning repo" {
		t.Errorf("head not kept: %q", out[0])
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "lines omitted") {
		t.Error("expected an omission marker between head and failed step")
	}
	if !strings.Contains(joined, "=> Step 2/3: Deploy") || !strings.Contains(joined, "error: terraform apply failed") {
		t.Error("failed step segment missing")
	}
	if strings.Contains(joined, "auth 150") {
		t.Error("middle of the log should be omitted")
	}
	if len(out) > 80 {
		t.Errorf("too many lines kept: %d", len(out))
	}
}

func TestTrimLogBudget(t *testing.T) {
	log := []string{"=> Step 1/1: Big"}
	log = append(log, lines(5000, "output line with some text")...)
	log = append(log, "FINAL ERROR")
	failed := 0
	out := TrimLog(log, &failed, 10_000)
	total := 0
	for _, l := range out {
		total += len(l) + 1
	}
	if total > 10_500 {
		t.Errorf("budget exceeded: %d chars", total)
	}
	if out[len(out)-1] != "FINAL ERROR" || out[0] != "=> Step 1/1: Big" {
		t.Errorf("segment ends lost: first=%q last=%q", out[0], out[len(out)-1])
	}
	if !strings.Contains(strings.Join(out, "\n"), "lines omitted") {
		t.Error("expected an omission marker inside the segment")
	}
}

func TestTrimLogBuildFailureAndRepeats(t *testing.T) {
	log := []string{"=> Cloning", "=> Building environment image from deploy/Dockerfile", "step 1/3", "retrying", "retrying", "retrying", "retrying", "ERROR: failed to solve"}
	out := TrimLog(log, nil, MaxLogChars)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "repeated 3 more times") {
		t.Errorf("repeats not collapsed:\n%s", joined)
	}
	if strings.Count(joined, "retrying") != 1 {
		t.Errorf("expected one retrying line:\n%s", joined)
	}
	long := TrimLog([]string{strings.Repeat("x", 1000)}, nil, MaxLogChars)
	if len(long[0]) > MaxLineChars+3 {
		t.Error("long line not shortened")
	}
}

func TestBuildUserMessage(t *testing.T) {
	failed, code := 1, 1
	run := Run{
		Org: "Acme", Repo: "api", Deployment: "prod", Target: "client-a", Trigger: "manual", StartedBy: "Ann",
		Status: "failed", Error: `step "Deploy" failed with exit code 1`, ExitCode: &code, FailedStep: &failed, Duration: "2m 3s",
		Dockerfile: "deploy/Dockerfile",
		Steps:      []Step{{"Auth", "gcloud auth", "succeeded"}, {"Deploy", "terraform apply", "failed"}, {"Verify", "curl x", "skipped"}},
		Inputs: []Input{
			{Name: "GCP_PROJECT", Label: "Project ID", Type: "text", Value: "acme-prod", Set: true, Source: "saved"},
			{Name: "GCP_KEY", Label: "Key", Type: "multiline", Set: true, Secret: true, Source: "saved"},
			{Name: "ACTION", Type: "select", Value: "destroy", Set: true, DeployTime: true, Source: "deploy"},
			{Name: "OPTIONAL", Source: "unset"},
		},
		Log: []string{"=> Step 2/3: Deploy", "Error: Permission denied on project"},
	}
	msg := BuildUserMessage(run)
	for _, want := range []string{
		"prod → target client-a", "manual by Ann", "at step 2 of 3, \"Deploy\"", "exit code 1",
		"### Step 2: Deploy — failed", "terraform apply",
		`GCP_PROJECT ("Project ID"): "acme-prod"`, "GCP_KEY (\"Key\"): <set, hidden> [multiline, secret]",
		`ACTION: "destroy" [select, chosen at deploy time]`, "OPTIONAL: <not set>",
		"Permission denied on project", "Explain this failure.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\n---\n%s", want, msg)
		}
	}
	msgs := BuildMessages(run)
	if len(msgs) != 2 || msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "## What to do next") {
		t.Error("system prompt missing")
	}
}
