package config

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// emailRe is a pragmatic email shape check used in addition to net/mail
// parsing (net/mail accepts addresses without a domain dot, display names,
// etc., which are not what a form field wants).
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// CheckValue validates one value against a variable's constraints and
// returns an empty string when valid, or a human-readable problem.
// An empty value is valid here — required-ness is checked by CheckValues,
// because "missing" and "invalid" are reported differently.
func CheckValue(v *Variable, value string) string {
	if value == "" {
		return ""
	}
	switch v.Type {
	case VarNumber:
		f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return "must be a number"
		}
		if v.Validation.Min != nil && f < *v.Validation.Min {
			return fmt.Sprintf("must be at least %v", formatNum(*v.Validation.Min))
		}
		if v.Validation.Max != nil && f > *v.Validation.Max {
			return fmt.Sprintf("must be at most %v", formatNum(*v.Validation.Max))
		}
	case VarEmail:
		if _, err := mail.ParseAddress(value); err != nil || !emailRe.MatchString(value) {
			return "must be a valid email address"
		}
	case VarURL:
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "must be a valid URL including the scheme (e.g. https://example.com)"
		}
	case VarBoolean:
		if value != "true" && value != "false" {
			return "must be true or false"
		}
	case VarSelect:
		for _, o := range v.Options {
			if o.Value == value {
				return ""
			}
		}
		return "must be one of the listed options"
	}

	if isTextual(v.Type) || v.Type == VarNumber {
		if v.Validation.MinLength != nil && len(value) < *v.Validation.MinLength {
			return fmt.Sprintf("must be at least %d characters", *v.Validation.MinLength)
		}
		if v.Validation.MaxLength != nil && len(value) > *v.Validation.MaxLength {
			return fmt.Sprintf("must be at most %d characters", *v.Validation.MaxLength)
		}
		if re := v.CompiledPattern(); re != nil && !re.MatchString(value) {
			if v.Validation.Message != "" {
				return v.Validation.Message
			}
			return fmt.Sprintf("must match pattern %s", v.Validation.Pattern)
		}
	}
	return ""
}

// ConstraintHints renders a variable's validation constraints as short
// human-readable phrases for form hints, phrased to match the errors
// CheckValue produces. Types whose widget already constrains the value
// (select, boolean) yield nothing.
func (v *Variable) ConstraintHints() []string {
	var hints []string
	switch v.Type {
	case VarNumber:
		switch {
		case v.Validation.Min != nil && v.Validation.Max != nil:
			hints = append(hints, fmt.Sprintf("a number between %s and %s", formatNum(*v.Validation.Min), formatNum(*v.Validation.Max)))
		case v.Validation.Min != nil:
			hints = append(hints, "a number ≥ "+formatNum(*v.Validation.Min))
		case v.Validation.Max != nil:
			hints = append(hints, "a number ≤ "+formatNum(*v.Validation.Max))
		}
	case VarEmail:
		hints = append(hints, "a valid email address")
	case VarURL:
		hints = append(hints, "a URL including the scheme (e.g. https://example.com)")
	}
	if isTextual(v.Type) {
		switch {
		case v.Validation.MinLength != nil && v.Validation.MaxLength != nil:
			hints = append(hints, fmt.Sprintf("%d–%d characters", *v.Validation.MinLength, *v.Validation.MaxLength))
		case v.Validation.MinLength != nil:
			hints = append(hints, fmt.Sprintf("at least %d characters", *v.Validation.MinLength))
		case v.Validation.MaxLength != nil:
			hints = append(hints, fmt.Sprintf("at most %d characters", *v.Validation.MaxLength))
		}
	}
	if v.Validation.Pattern != "" && (isTextual(v.Type) || v.Type == VarNumber) {
		if v.Validation.Message != "" {
			hints = append(hints, v.Validation.Message)
		} else {
			hints = append(hints, "matching pattern "+v.Validation.Pattern)
		}
	}
	return hints
}

// CompiledPattern returns the compiled validation pattern, compiling it on
// first use for variables constructed outside of Parse.
func (v *Variable) CompiledPattern() *regexp.Regexp {
	if v.Validation.compiled == nil && v.Validation.Pattern != "" {
		re, err := regexp.Compile("^(?:" + v.Validation.Pattern + ")$")
		if err == nil {
			v.Validation.compiled = re
		}
	}
	return v.Validation.compiled
}

// ValueProblem describes one invalid or missing variable value.
type ValueProblem struct {
	Name    string
	Message string
}

// CheckValues validates a full set of values (saved configuration merged
// with any deploy-time values) against a deployment's variables. Missing
// required values (after applying defaults) and invalid values are both
// reported; inactive variables — whose `when:` condition does not hold —
// are skipped entirely. Unknown names in values are reported too, so a
// stale saved value never silently leaks into a run.
func CheckValues(d *Deployment, values map[string]string) []ValueProblem {
	return checkValues(d, values, false)
}

// CheckConfigValues is CheckValues restricted to a target's stored
// configuration: deploy-time variables are excluded, since their values are
// supplied when a deploy starts. Use it for readiness and the target form.
func CheckConfigValues(d *Deployment, values map[string]string) []ValueProblem {
	return checkValues(d, values, true)
}

func checkValues(d *Deployment, values map[string]string, configOnly bool) []ValueProblem {
	active := d.ActiveSet(values)
	var problems []ValueProblem
	for _, v := range d.Variables {
		if !active[v.Name] || (configOnly && v.DeployTime) {
			continue
		}
		val, ok := values[v.Name]
		if !ok || val == "" {
			if v.Default != "" {
				continue
			}
			if v.Required {
				problems = append(problems, ValueProblem{v.Name, "is required"})
			}
			continue
		}
		if msg := CheckValue(v, val); msg != "" {
			problems = append(problems, ValueProblem{v.Name, msg})
		}
	}
	for name := range values {
		if d.Variable(name) == nil {
			problems = append(problems, ValueProblem{name, "is not declared in this deployment's variables"})
		}
	}
	return problems
}

// ResolveValues merges defaults with the provided values and returns the
// final environment map for a run. Active boolean variables that resolve to
// empty become "false" so the step scripts always see a defined value.
// Inactive variables (whose `when:` condition does not hold) are excluded —
// step scripts should read them as `${NAME:-}`.
func ResolveValues(d *Deployment, values map[string]string) map[string]string {
	active := d.ActiveSet(values)
	out := make(map[string]string, len(d.Variables))
	for _, v := range d.Variables {
		if !active[v.Name] {
			continue
		}
		val := values[v.Name]
		if val == "" {
			val = v.Default
		}
		if val == "" && v.Type == VarBoolean {
			val = "false"
		}
		if val == "" && !v.Required {
			continue
		}
		out[v.Name] = val
	}
	return out
}

func formatNum(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
