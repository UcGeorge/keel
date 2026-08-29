// Package config defines the keel.yaml deployment configuration schema,
// its parser, and its validator.
//
// A keel.yaml file describes one or more deployments. Each deployment
// defines the environment it runs in (a Dockerfile), the ordered steps
// executed inside that environment, and the variables that must be
// provided before it can run.
package config

import (
	"regexp"
	"strconv"
	"strings"
)

// FileNames are the configuration file names Keel recognizes, in order of
// preference.
var FileNames = []string{"keel.yaml", "keel.yml"}

// DefaultFileName is the canonical configuration file name.
const DefaultFileName = "keel.yaml"

// SupportedVersion is the keel.yaml schema version this build understands.
const SupportedVersion = 1

// ReservedVarPrefix is reserved for metadata Keel injects into every run
// (KEEL_DEPLOYMENT, KEEL_TARGET, KEEL_TARGET_ID, KEEL_RUN_ID). User variables
// may not use it.
const ReservedVarPrefix = "KEEL_"

// VarType enumerates the input types a variable can declare. The type drives
// both validation and how the variable's form field is rendered.
type VarType string

const (
	VarText      VarType = "text"
	VarMultiline VarType = "multiline"
	VarNumber    VarType = "number"
	VarEmail     VarType = "email"
	VarURL       VarType = "url"
	VarBoolean   VarType = "boolean"
	VarSelect    VarType = "select"
)

// VarTypes lists every valid variable type.
var VarTypes = []VarType{VarText, VarMultiline, VarNumber, VarEmail, VarURL, VarBoolean, VarSelect}

// Config is a parsed keel.yaml document.
type Config struct {
	Version     int           `yaml:"version"`
	Deployments []*Deployment `yaml:"-"`

	// Raw holds the original document bytes, if the config was loaded from
	// a file or byte slice.
	Raw []byte `yaml:"-"`
}

// Deployment returns the deployment with the given name, or nil.
func (c *Config) Deployment(name string) *Deployment {
	for _, d := range c.Deployments {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// DeploymentNames returns the deployment names in document order.
func (c *Config) DeploymentNames() []string {
	names := make([]string, len(c.Deployments))
	for i, d := range c.Deployments {
		names[i] = d.Name
	}
	return names
}

// Deployment is one top-level entry under `deployments:`.
type Deployment struct {
	// Name is the map key under `deployments:`.
	Name string

	// Description is the short summary shown in deployment lists and the
	// manifest. Plain text — it is rendered where markdown would not fit.
	Description string

	// LongDescription is the full description shown on the deployment's own
	// page. Markdown is supported.
	LongDescription string

	// Groups define the variable groups referenced by Variable.Group,
	// in document order. Groups render in ascending ID order.
	Groups []VarGroup

	// Environment defines where the deployment steps run.
	Environment Environment

	// Steps are executed in order inside the environment container.
	Steps []Step

	// Variables are the inputs a deployment target must provide,
	// in document order.
	Variables []*Variable

	// Outputs are environment variables captured from the container at the
	// end of a fully successful run, in document order. Steps publish them
	// with plain `export NAME=value`.
	Outputs []*Output
}

// Output returns the output with the given name, or nil.
func (d *Deployment) Output(name string) *Output {
	for _, o := range d.Outputs {
		if o.Name == name {
			return o
		}
	}
	return nil
}

// OutputNames returns the output names in document order.
func (d *Deployment) OutputNames() []string {
	names := make([]string, len(d.Outputs))
	for i, o := range d.Outputs {
		names[i] = o.Name
	}
	return names
}

// Output declares one run output: an environment variable read from the
// container when a run finishes successfully.
type Output struct {
	// Name is the map key under `outputs:` — the environment variable to
	// capture. Same naming rules as variables.
	Name string

	// Label is the human-readable label. Defaults to the name.
	Label string

	// Description explains what the value is. Markdown is supported.
	Description string

	// Secret hides the value behind a reveal control and stores it like a
	// secret. Values matching a secret input are treated as secret even
	// without this flag.
	Secret bool
}

// EffectiveLabel returns the label, falling back to the output name.
func (o *Output) EffectiveLabel() string {
	if strings.TrimSpace(o.Label) != "" {
		return o.Label
	}
	return o.Name
}

// Group returns the group definition with the given ID, or nil. Variables
// may reference IDs with no definition; such groups render with a fallback
// label.
func (d *Deployment) Group(id int) *VarGroup {
	for i := range d.Groups {
		if d.Groups[i].ID == id {
			return &d.Groups[i]
		}
	}
	return nil
}

// VarGroup is one entry under a deployment's `groups:` mapping. It gives a
// variable group (referenced by Variable.Group) its label and UI behavior.
type VarGroup struct {
	// ID is the mapping key: an integer. Groups render in ascending ID
	// order, after all ungrouped variables.
	ID int

	// Label is the group heading. Defaults to "Group <ID>".
	Label string

	// Description is shown under the group heading. Markdown is supported.
	Description string

	// Collapsed makes the group start collapsed in the UI.
	Collapsed bool
}

// EffectiveLabel returns the label, falling back to "Group <ID>".
func (g VarGroup) EffectiveLabel() string {
	if strings.TrimSpace(g.Label) != "" {
		return g.Label
	}
	return "Group " + strconv.Itoa(g.ID)
}

// Variable returns the variable with the given name, or nil.
func (d *Deployment) Variable(name string) *Variable {
	for _, v := range d.Variables {
		if v.Name == name {
			return v
		}
	}
	return nil
}

// SecretNames returns the names of all secret variables.
func (d *Deployment) SecretNames() []string {
	var names []string
	for _, v := range d.Variables {
		if v.Secret {
			names = append(names, v.Name)
		}
	}
	return names
}

// Environment describes the container image a deployment executes in.
type Environment struct {
	// Dockerfile is the path to the Dockerfile, relative to the repository
	// root. Required.
	Dockerfile string

	// Context is the Docker build context, relative to the repository root.
	// Defaults to ".".
	Context string
}

// Step is a single named command executed inside the environment.
type Step struct {
	// Name is a short human-readable label. Required.
	Name string

	// Run is the shell command (may be multi-line). Required.
	Run string
}

// Variable declares one input for a deployment.
type Variable struct {
	// Name is the map key under `variables:` and becomes the environment
	// variable name inside the run. Must match ^[A-Z][A-Z0-9_]*$.
	Name string

	// Label is the human-readable field label. Defaults to the name.
	Label string

	// Type drives validation and form rendering. Defaults to "text".
	Type VarType

	// Secret marks the value as sensitive: it is stored encrypted, rendered
	// as a password input, and masked in logs.
	Secret bool

	// Required marks the variable as mandatory. Defaults to true.
	Required bool

	// Description explains what the variable is, shown under the form field.
	// Markdown is supported.
	Description string

	// Placeholder is the form field placeholder text.
	Placeholder string

	// Default is the pre-filled value. Not allowed for secrets.
	Default string

	// Options are the allowed values for type "select".
	Options []Option

	// Validation holds extra validation constraints.
	Validation Validation

	// Manifest controls how the variable appears in a generated variable
	// manifest document.
	Manifest ManifestSpec

	// Group places the variable in the group with this ID (see
	// Deployment.Groups). Ungrouped variables render before all groups.
	Group *int

	// Row places the variable on a horizontal row: variables sharing a row
	// ID (within the same group) render side by side, rows in ascending ID
	// order. Variables without a row render last in their group, full width.
	Row *int

	// Flex is the variable's share of its row's width, CSS flex-grow style.
	// Zero means unset (treated as 1). Requires Row.
	Flex float64

	// DeployTime asks for the value every time a deploy is started (in the
	// UI, via a modal on the Deploy button) instead of storing it in the
	// target's configuration. Defaults come from the variable's default.
	DeployTime bool

	// When gates the variable on another variable's value: while the
	// condition does not hold the variable is inactive — disabled in forms,
	// never required, and not exported into the run.
	When *Condition
}

// NeedsInput reports whether the variable must be given a value by the
// user: it is required and has no default to fall back to. Drives the
// required markers in forms.
func (v *Variable) NeedsInput() bool {
	return v.Required && v.Default == ""
}

// EffectiveFlex returns the flex share, defaulting to 1.
func (v *Variable) EffectiveFlex() float64 {
	if v.Flex > 0 {
		return v.Flex
	}
	return 1
}

// Condition ops. Ordering operators compare numerically.
const (
	CondEq  = "eq"
	CondNe  = "ne"
	CondIn  = "in"
	CondGt  = "gt"
	CondGte = "gte"
	CondLt  = "lt"
	CondLte = "lte"
	CondSet = "set"
)

// CondOps lists every condition operator.
var CondOps = []string{CondEq, CondNe, CondIn, CondGt, CondGte, CondLt, CondLte, CondSet}

// Condition is a variable's `when:` clause — a single comparison against
// another variable's effective value (its value, or its default). Chains
// arise naturally: the referenced variable may itself carry a condition,
// and an inactive referenced variable makes every condition on it false.
type Condition struct {
	// Var is the variable the condition reads.
	Var string
	// Op is one of CondOps.
	Op string
	// Value is the operand for eq/ne/gt/gte/lt/lte.
	Value string
	// Values is the operand list for in.
	Values []string
	// Want is the operand for set: true = must have a value, false = must
	// be empty.
	Want bool
}

// Holds reports whether the condition holds for the referenced variable's
// effective value ("" means unset).
func (c *Condition) Holds(refValue string) bool {
	if c.Op == CondSet {
		return (refValue != "") == c.Want
	}
	if refValue == "" {
		return false
	}
	switch c.Op {
	case CondEq:
		return refValue == c.Value
	case CondNe:
		return refValue != c.Value
	case CondIn:
		for _, v := range c.Values {
			if refValue == v {
				return true
			}
		}
		return false
	case CondGt, CondGte, CondLt, CondLte:
		a, err1 := strconv.ParseFloat(strings.TrimSpace(refValue), 64)
		b, err2 := strconv.ParseFloat(strings.TrimSpace(c.Value), 64)
		if err1 != nil || err2 != nil {
			return false
		}
		switch c.Op {
		case CondGt:
			return a > b
		case CondGte:
			return a >= b
		case CondLt:
			return a < b
		case CondLte:
			return a <= b
		}
	}
	return false
}

// Describe renders the condition as a short human-readable phrase for form
// hints and manifests, e.g. `ACTION = destroy`.
func (c *Condition) Describe() string {
	switch c.Op {
	case CondEq:
		return c.Var + " = " + c.Value
	case CondNe:
		return c.Var + " ≠ " + c.Value
	case CondIn:
		return c.Var + " is one of " + strings.Join(c.Values, ", ")
	case CondGt:
		return c.Var + " > " + c.Value
	case CondGte:
		return c.Var + " ≥ " + c.Value
	case CondLt:
		return c.Var + " < " + c.Value
	case CondLte:
		return c.Var + " ≤ " + c.Value
	case CondSet:
		if c.Want {
			return c.Var + " is set"
		}
		return c.Var + " is empty"
	}
	return c.Var
}

// DeployTimeVariables returns the variables asked for on every deploy, in
// document order.
func (d *Deployment) DeployTimeVariables() []*Variable {
	var out []*Variable
	for _, v := range d.Variables {
		if v.DeployTime {
			out = append(out, v)
		}
	}
	return out
}

// ConfigVariables returns the variables stored in a target's configuration
// (everything that is not deploy-time), in document order.
func (d *Deployment) ConfigVariables() []*Variable {
	var out []*Variable
	for _, v := range d.Variables {
		if !v.DeployTime {
			out = append(out, v)
		}
	}
	return out
}

// EffectiveValue returns the variable's value from values, falling back to
// its default.
func EffectiveValue(v *Variable, values map[string]string) string {
	if val := values[v.Name]; val != "" {
		return val
	}
	return v.Default
}

// ActiveSet evaluates every variable's condition against the given values
// (each variable's effective value is its entry in values, or its default)
// and reports which variables are active. A variable whose condition reads
// an inactive or unknown variable is inactive. Cycles are rejected by
// validation; if one slips through, its members evaluate inactive rather
// than looping.
func (d *Deployment) ActiveSet(values map[string]string) map[string]bool {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(d.Variables))
	active := make(map[string]bool, len(d.Variables))
	var eval func(v *Variable) bool
	eval = func(v *Variable) bool {
		switch state[v.Name] {
		case visiting:
			return false
		case done:
			return active[v.Name]
		}
		state[v.Name] = visiting
		ok := true
		if v.When != nil {
			ref := d.Variable(v.When.Var)
			if ref == nil || !eval(ref) {
				ok = false
			} else {
				ok = v.When.Holds(EffectiveValue(ref, values))
			}
		}
		state[v.Name] = done
		active[v.Name] = ok
		return ok
	}
	for _, v := range d.Variables {
		eval(v)
	}
	return active
}

// EffectiveLabel returns the label, falling back to the variable name.
func (v *Variable) EffectiveLabel() string {
	if strings.TrimSpace(v.Label) != "" {
		return v.Label
	}
	return v.Name
}

// Option is one allowed value of a select variable.
type Option struct {
	// Value is the stored/exported value. Required.
	Value string
	// Label is the display label. Defaults to the value.
	Label string
}

// EffectiveLabel returns the option label, falling back to its value.
func (o Option) EffectiveLabel() string {
	if strings.TrimSpace(o.Label) != "" {
		return o.Label
	}
	return o.Value
}

// Validation holds the optional validation constraints of a variable.
type Validation struct {
	// Pattern is a RE2 regular expression the value must match entirely.
	Pattern string
	// Message overrides the error message when the pattern does not match.
	Message string
	// Min and Max bound numeric values (inclusive).
	Min *float64
	// MinLength and MaxLength bound the length of text-like values.
	Max       *float64
	MinLength *int
	MaxLength *int

	// compiled pattern, populated by Validate.
	compiled *regexp.Regexp
}

// ManifestSpec controls a variable's entry in the generated variable
// manifest — the document handed to a third party that explains which
// values are needed, why, and how to obtain them.
type ManifestSpec struct {
	// Include marks the variable as part of the default manifest selection.
	// Defaults to true when a manifest block is present, false otherwise.
	Include bool

	// Title overrides the entry heading. Defaults to the variable label.
	Title string

	// Why explains why the value is needed. Markdown is supported.
	Why string

	// How explains how to obtain the value. Markdown is supported.
	How string
}

// varNameRe validates variable names: uppercase environment-variable style.
var varNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// deploymentNameRe validates deployment and target names: lowercase
// DNS-label style, matching how services are named in compose files.
var deploymentNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ValidVarName reports whether name is a valid variable name.
func ValidVarName(name string) bool {
	return varNameRe.MatchString(name) && !strings.HasPrefix(name, ReservedVarPrefix)
}

// ValidDeploymentName reports whether name is a valid deployment name.
func ValidDeploymentName(name string) bool {
	return len(name) <= 63 && deploymentNameRe.MatchString(name)
}

// ValidTargetName reports whether name is a valid deployment target name.
// Targets follow the same naming rules as deployments.
func ValidTargetName(name string) bool {
	return ValidDeploymentName(name)
}
