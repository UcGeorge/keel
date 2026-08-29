// Package insight explains failed runs with a language model.
//
// It assembles everything relevant to a failure — the deployment's
// definition, the values the run received (secrets hidden), and the part
// of the log that matters — into a prompt that fits comfortably in any
// current model's context, asks for an explanation written for the person
// who pressed Deploy, and returns Markdown.
package insight

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/UcGeorge/keel/internal/llm"
)

// Budgets, in characters (roughly four per token). The total stays under
// ~16k tokens so 32k-context models have room to answer.
const (
	MaxLogChars    = 40_000
	MaxStepChars   = 1_500
	MaxLineChars   = 400
	MaxReplyTokens = 1_800
)

// Step is one deployment step with its outcome.
type Step struct {
	Name   string
	Run    string
	Status string
}

// Input is one variable value the run received. Secret values are never
// present; Set says whether one existed.
type Input struct {
	Name        string
	Label       string
	Type        string
	Description string
	Value       string
	Set         bool
	Secret      bool
	DeployTime  bool
	Source      string
}

// Run is the context of one failed run.
type Run struct {
	Org, Repo, Deployment, Target string
	Trigger, StartedBy, CommitSHA string
	Status, Error                 string
	ExitCode                      *int
	FailedStep                    *int // 0-based; nil when no step ran
	Duration                      string
	Dockerfile                    string
	Description                   string
	Steps                         []Step
	Inputs                        []Input
	Log                           []string
}

// SystemPrompt frames the task for the model.
const SystemPrompt = `You are the failure analyst inside Keel, a tool that lets non-technical operators run deployments through a form. A deployment run has failed and the person reading your answer is usually the operator who pressed Deploy — not the engineer who wrote the deployment. Your job is to bridge the gap between the technical log and what they need to understand and do.

Write in Markdown with exactly these sections:

## What happened
Two or three plain-language sentences. No jargon; name the step that failed and what it was trying to do.

## Likely cause
The most probable root cause, with the evidence from the log quoted briefly (one or two short lines). If several causes are plausible, list them in order of likelihood. If the log does not show the cause, say so plainly rather than guessing.

## What to do next
A short numbered list of concrete actions the operator can take — which value to check on the target's form, what to ask the engineer or the cloud account owner for, whether re-running is safe. Point to specific variable labels when a value is involved.

## For the engineer
Technical detail: the failing command, the exit code, the exact error, and what to change in the deployment if the fix belongs in the repository.

Rules: base everything on the run context you are given; never invent log lines or values. Secret values are hidden from you and must never be guessed or asked for. Keep the whole answer under 350 words. Do not add a title or any section other than the four above.`

// Explain runs the analysis with the given client and model.
func Explain(ctx context.Context, client *llm.Client, model string, run Run) (string, error) {
	msgs := BuildMessages(run)
	return client.Chat(ctx, model, msgs, MaxReplyTokens)
}

// BuildMessages renders the run into the chat messages sent to the model.
func BuildMessages(run Run) []llm.Message {
	return []llm.Message{
		{Role: "system", Content: SystemPrompt},
		{Role: "user", Content: BuildUserMessage(run)},
	}
}

// BuildUserMessage renders the run context as Markdown.
func BuildUserMessage(run Run) string {
	var b strings.Builder
	b.WriteString("# Failed run\n\n")
	fmt.Fprintf(&b, "- Organization / repository: %s / %s\n", run.Org, run.Repo)
	fmt.Fprintf(&b, "- Deployment: %s → target %s\n", run.Deployment, run.Target)
	if run.Description != "" {
		fmt.Fprintf(&b, "- Deployment description: %s\n", strings.TrimSpace(run.Description))
	}
	trigger := run.Trigger
	if run.StartedBy != "" {
		trigger += " by " + run.StartedBy
	}
	if run.CommitSHA != "" {
		trigger += fmt.Sprintf(" (commit %.7s)", run.CommitSHA)
	}
	fmt.Fprintf(&b, "- Trigger: %s\n", trigger)
	outcome := run.Status
	if run.FailedStep != nil && *run.FailedStep >= 0 && *run.FailedStep < len(run.Steps) {
		outcome += fmt.Sprintf(" at step %d of %d, %q", *run.FailedStep+1, len(run.Steps), run.Steps[*run.FailedStep].Name)
	} else if run.Status == "failed" {
		outcome += " before any step ran (clone or environment image build)"
	}
	if run.ExitCode != nil {
		outcome += fmt.Sprintf(", exit code %d", *run.ExitCode)
	}
	fmt.Fprintf(&b, "- Outcome: %s\n", outcome)
	if run.Error != "" {
		fmt.Fprintf(&b, "- Error recorded by Keel: %s\n", run.Error)
	}
	if run.Duration != "" {
		fmt.Fprintf(&b, "- Duration: %s\n", run.Duration)
	}

	b.WriteString("\n## Deployment definition (from keel.yaml)\n\n")
	if run.Dockerfile != "" {
		fmt.Fprintf(&b, "Environment image: `%s` (steps run with /bin/sh inside a container built from it, repository mounted at /workspace).\n\n", run.Dockerfile)
	}
	for i, st := range run.Steps {
		status := st.Status
		if status == "" {
			status = "unknown"
		}
		fmt.Fprintf(&b, "### Step %d: %s — %s\n\n```sh\n%s\n```\n\n", i+1, st.Name, status, clip(strings.TrimSpace(st.Run), MaxStepChars))
	}

	if len(run.Inputs) > 0 {
		b.WriteString("## Inputs the run received (secret values are hidden)\n\n")
		for _, in := range run.Inputs {
			var attrs []string
			if in.Type != "" {
				attrs = append(attrs, in.Type)
			}
			if in.DeployTime {
				attrs = append(attrs, "chosen at deploy time")
			}
			if in.Secret {
				attrs = append(attrs, "secret")
			}
			label := in.Name
			if in.Label != "" && in.Label != in.Name {
				label = fmt.Sprintf("%s (%q)", in.Name, in.Label)
			}
			var value string
			switch {
			case in.Source == "inactive":
				value = "<inactive — its condition did not hold, not exported>"
			case !in.Set:
				value = "<not set>"
			case in.Secret:
				value = "<set, hidden>"
			default:
				value = strconv.Quote(clip(in.Value, 300))
			}
			line := fmt.Sprintf("- %s: %s", label, value)
			if len(attrs) > 0 {
				line += " [" + strings.Join(attrs, ", ") + "]"
			}
			if in.Description != "" {
				line += " — " + clip(oneLine(in.Description), 160)
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Log\n\n")
	trimmed := TrimLog(run.Log, run.FailedStep, MaxLogChars)
	if len(trimmed) < len(run.Log) {
		fmt.Fprintf(&b, "(%d of %d lines; the beginning and the failing part are kept, the middle is omitted)\n\n", len(trimmed), len(run.Log))
	}
	b.WriteString("```\n")
	for _, l := range trimmed {
		b.WriteString(l + "\n")
	}
	b.WriteString("```\n\nExplain this failure.")
	return b.String()
}

var stepBegin = regexp.MustCompile(`^=> Step (\d+)/\d+: `)

// TrimLog selects the lines worth sending: the head of the log (clone and
// image build start), then everything from the failed step's first line
// on — or from the image build when no step ran — with the middle of an
// oversized segment cut out so the beginning and the end (where the error
// usually is) both survive. Lines are shortened, and consecutive
// repeats collapse into one line with a count.
func TrimLog(lines []string, failedStep *int, maxChars int) []string {
	lines = collapseRepeats(shortenLines(lines))
	if len(lines) == 0 {
		return nil
	}
	const head = 12
	start := segmentStart(lines, failedStep)
	if start < head {
		start = 0
	}
	var out []string
	if start > 0 {
		out = append(out, lines[:head]...)
		if start > head {
			out = append(out, fmt.Sprintf("[… %d lines omitted …]", start-head))
		}
	}
	segment := lines[start:]
	budget := maxChars - charCount(out)
	if charCount(segment) <= budget {
		return append(out, segment...)
	}
	// Keep the first lines of the segment (the command that ran) and as
	// much of its end as fits.
	const keepFirst = 40
	first := segment
	if len(first) > keepFirst {
		first = segment[:keepFirst]
	}
	budget -= charCount(first) + 40
	tailStart := len(segment)
	used := 0
	for tailStart > len(first) {
		n := len(segment[tailStart-1]) + 1
		if used+n > budget {
			break
		}
		used += n
		tailStart--
	}
	out = append(out, first...)
	if tailStart > len(first) {
		out = append(out, fmt.Sprintf("[… %d lines omitted …]", tailStart-len(first)))
	}
	return append(out, segment[tailStart:]...)
}

// segmentStart finds where the relevant part of the log begins.
func segmentStart(lines []string, failedStep *int) int {
	if failedStep != nil && *failedStep >= 0 {
		want := strconv.Itoa(*failedStep + 1)
		for i, l := range lines {
			if m := stepBegin.FindStringSubmatch(l); m != nil && m[1] == want {
				return i
			}
		}
	}
	// No step ran: the environment image build (or the clone) failed.
	for i, l := range lines {
		if strings.HasPrefix(l, "=> Building environment image") {
			return i
		}
	}
	return 0
}

func shortenLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = clip(l, MaxLineChars)
	}
	return out
}

func collapseRepeats(lines []string) []string {
	var out []string
	for i := 0; i < len(lines); i++ {
		j := i
		for j+1 < len(lines) && lines[j+1] == lines[i] {
			j++
		}
		out = append(out, lines[i])
		if j > i+1 {
			out = append(out, fmt.Sprintf("[… previous line repeated %d more times …]", j-i))
		} else if j == i+1 {
			out = append(out, lines[i])
		}
		i = j
	}
	return out
}

func charCount(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len(l) + 1
	}
	return n
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
