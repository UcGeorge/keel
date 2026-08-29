package web

import (
	"github.com/UcGeorge/keel/internal/config"
)

// Input sources: where a run's value for a variable came from.
const (
	InputSaved    = "saved"    // the target's stored value
	InputDeploy   = "deploy"   // typed into the deploy modal (or passed on the CLI)
	InputDefault  = "default"  // the variable's default
	InputUnset    = "unset"    // optional, no value, not exported
	InputInactive = "inactive" // its `when:` condition did not hold; not exported
)

// RunInputSnapshot is one variable value a run is started with, as
// persisted next to the run. Secrets never carry their value — Value is
// empty and Secret is true — so a run page can say a credential was set
// without ever becoming a way to read it.
type RunInputSnapshot struct {
	Idx        int
	Name       string
	Label      string
	Value      string
	Secret     bool
	DeployTime bool
	Source     string
}

// SnapshotInputs computes what to record for a run started from values
// (the target's saved values merged with deploy-time values). The value
// recorded is the one the run's environment receives.
func SnapshotInputs(d *config.Deployment, values map[string]string) []RunInputSnapshot {
	active := d.ActiveSet(values)
	env := config.ResolveValues(d, values)
	out := make([]RunInputSnapshot, 0, len(d.Variables))
	for i, v := range d.Variables {
		in := RunInputSnapshot{Idx: i, Name: v.Name, Label: v.EffectiveLabel(), Secret: v.Secret, DeployTime: v.DeployTime}
		switch {
		case !active[v.Name]:
			in.Source = InputInactive
		case values[v.Name] != "":
			in.Source = InputSaved
			if v.DeployTime {
				in.Source = InputDeploy
			}
		case env[v.Name] != "":
			in.Source = InputDefault // declared default, or a boolean's implicit false
		default:
			in.Source = InputUnset
		}
		if !v.Secret && (in.Source == InputSaved || in.Source == InputDeploy || in.Source == InputDefault) {
			in.Value = env[v.Name]
		}
		out = append(out, in)
	}
	return out
}

// RunInputVM is one input as the run page renders it.
type RunInputVM struct {
	Name       string
	Label      string
	Value      string
	Secret     bool
	DeployTime bool
	Source     string
}

// Set reports whether the run received a value for this input.
func (in RunInputVM) Set() bool {
	return in.Source == InputSaved || in.Source == InputDeploy || in.Source == InputDefault
}

// RunInputsVM groups a run's inputs for the run page: the values chosen
// when the deploy started, then the target's configuration.
type RunInputsVM struct {
	DeployTime []RunInputVM
	Config     []RunInputVM
}

// Empty reports whether there is nothing to show.
func (v *RunInputsVM) Empty() bool {
	return v == nil || (len(v.DeployTime) == 0 && len(v.Config) == 0)
}

// BuildRunInputsVM splits stored inputs (in declaration order) into the
// two sections of the run page.
func BuildRunInputsVM(inputs []RunInputVM) *RunInputsVM {
	if len(inputs) == 0 {
		return nil
	}
	vm := &RunInputsVM{}
	for _, in := range inputs {
		if in.DeployTime {
			vm.DeployTime = append(vm.DeployTime, in)
		} else {
			vm.Config = append(vm.Config, in)
		}
	}
	return vm
}

// RunInputChip is a compact NAME=value shown in run tables so a run's
// deploy-time choices — plan or apply, deploy or destroy, which image tag —
// are visible at a glance.
type RunInputChip struct {
	Name  string
	Value string
}

// ChipValue shortens long values for the table.
func (c RunInputChip) ChipValue() string {
	r := []rune(c.Value)
	if len(r) <= 24 {
		return c.Value
	}
	return string(r[:23]) + "…"
}
