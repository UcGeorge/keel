package web

import (
	"sort"
	"time"

	"github.com/UcGeorge/keel/internal/config"
)

// DeploymentVM is a deployment as the templates see it, identical in both
// UIs. URLBase is the deployment's page URL; target and manifest links are
// derived from it.
type DeploymentVM struct {
	Name            string
	Description     string
	LongDescription string
	Dockerfile      string
	Context         string
	Steps           []config.Step
	Variables       []*config.Variable
	Outputs         []*config.Output
	Targets         []TargetVM
	URLBase         string
}

// HasDeployTimeVars reports whether any variable is asked for at deploy
// time — deploying such a deployment needs the deploy modal.
func (d *DeploymentVM) HasDeployTimeVars() bool {
	for _, v := range d.Variables {
		if v.DeployTime {
			return true
		}
	}
	return false
}

// TargetVM is a deployment target row.
type TargetVM struct {
	ID          string
	Deployment  string
	Name        string
	Description string
	AutoDeploy  bool
	URL         string
	// VarsSet / VarsTotal summarize configuration completeness (deploy-time
	// variables excluded — they are provided when a deploy starts).
	VarsSet   int
	VarsTotal int
	// Ready means every required active configuration variable has a value
	// (or default); Missing counts the values still needed or invalid.
	Ready    bool
	Missing  int
	LastRun  *RunVM
	Editable bool
}

// VarFieldVM is one variable form field with its current state.
type VarFieldVM struct {
	V *config.Variable
	// Value is the current saved value; secrets never carry their value
	// here, HasSaved says one exists.
	Value    string
	HasSaved bool
	Error    string
	// Active reports whether the variable's `when:` condition currently
	// holds (true for unconditional variables). Inactive fields render
	// disabled; the form script re-evaluates live as values change.
	Active bool
}

// VarRowVM is one horizontal row of variable fields. Full reports whether
// the row should stretch to the form's full width (single, rowless fields).
type VarRowVM struct {
	Fields []VarFieldVM
	Full   bool
}

// VarGroupVM is one collapsible group of variable rows.
type VarGroupVM struct {
	ID          int
	Label       string
	Description string
	Collapsed   bool
	Rows        []VarRowVM
	Count       int
	// RequiredCount counts the group's fields that must be filled in
	// (required, no default), so collapsed groups stay scannable.
	RequiredCount int
}

// VarLayoutVM arranges a deployment's variable fields into the layout
// declared in keel.yaml: ungrouped variables first, then groups in
// ascending ID order; within each, explicit rows in ascending row ID,
// then rowless variables full-width in document order.
type VarLayoutVM struct {
	Ungrouped []VarRowVM
	Groups    []VarGroupVM
}

// OutputVM is one run output as the templates render it.
type OutputVM struct {
	Name        string
	Label       string
	Description string
	Secret      bool
	// Set reports the value was captured on this run; Value carries it —
	// except for secrets the viewer may not reveal, which stay empty.
	Set   bool
	Value string
	// CanReveal marks a secret value that is present and revealable.
	CanReveal bool
}

// StoredOutput is one persisted, decrypted output value.
type StoredOutput struct {
	Value  string
	Secret bool
}

// BuildOutputVMs orders a run's stored outputs by the deployment's
// declaration, marking declared-but-missing outputs as not set. Outputs no
// longer declared in the config still render, after the declared ones, so
// captured values never silently disappear. canReveal gates whether secret
// values are put in the page at all.
func BuildOutputVMs(d *config.Deployment, stored map[string]StoredOutput, canReveal bool) []OutputVM {
	var out []OutputVM
	seen := map[string]bool{}
	add := func(vm OutputVM, s StoredOutput, ok bool) {
		if ok {
			vm.Set = true
			vm.Secret = vm.Secret || s.Secret
			if !vm.Secret {
				vm.Value = s.Value
			} else if canReveal {
				vm.Value = s.Value
				vm.CanReveal = true
			}
		}
		out = append(out, vm)
	}
	if d != nil {
		for _, o := range d.Outputs {
			seen[o.Name] = true
			s, ok := stored[o.Name]
			add(OutputVM{Name: o.Name, Label: o.EffectiveLabel(), Description: o.Description, Secret: o.Secret}, s, ok)
		}
	}
	var rest []string
	for name := range stored {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		add(OutputVM{Name: name, Label: name}, stored[name], true)
	}
	return out
}

// TargetFormVM is the create/edit target form.
type TargetFormVM struct {
	Action      string
	Name        string
	Description string
	AutoDeploy  bool
	ShowAuto    bool
	NameError   string
	Submit      string
}

// RunVM is a run as rendered in tables and on the run page.
type RunVM struct {
	ID         string
	Deployment string
	TargetName string
	RepoName   string // cloud only
	Status     string
	Trigger    string // "manual" | "push"
	CommitSHA  string
	StartedBy  string
	Error      string
	ExitCode   *int
	FailedStep *int
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	URL        string
	CancelURL  string
	Active     bool
	// Inputs are the run's non-secret deploy-time values, for tables.
	Inputs []RunInputChip
}

// Duration returns the elapsed run time (live for active runs).
func (r RunVM) Duration() time.Duration {
	if r.StartedAt == nil {
		return 0
	}
	end := time.Now()
	if r.FinishedAt != nil {
		end = *r.FinishedAt
	}
	return end.Sub(*r.StartedAt)
}

// StepVM is one run step row with live status.
type StepVM struct {
	Idx    int
	Name   string
	Status string
}

// ConfigStatusVM summarizes configuration validity for banners.
type ConfigStatusVM struct {
	OK     bool
	Source string // file path (dev) or "branch@sha" (cloud)
	Errors []config.ValidationError
	// Missing is true when no keel.yaml exists at all.
	Missing bool
}

// ManifestBuilderVM is the manifest builder page.
type ManifestBuilderVM struct {
	Deployment  *DeploymentVM
	Selected    map[string]bool
	Preview     string // rendered HTML of the generated markdown
	DownloadURL string
	FormAction  string
}

// NewDeploymentVM builds the template view of a configured deployment.
func NewDeploymentVM(d *config.Deployment, urlBase string) *DeploymentVM {
	return &DeploymentVM{
		Name:            d.Name,
		Description:     d.Description,
		LongDescription: d.LongDescription,
		Dockerfile:      d.Environment.Dockerfile,
		Context:         d.Environment.Context,
		Steps:           d.Steps,
		Variables:       d.Variables,
		Outputs:         d.Outputs,
		URLBase:         urlBase,
	}
}

// BuildVarFields assembles form fields for the given subset of a
// deployment's variables (d.ConfigVariables() for the target form,
// d.DeployTimeVariables() for the deploy modal), given the current values
// (secrets get HasSaved, never a value) and per-field errors. Conditions
// are evaluated against values, so pass the merged view the form will see.
func BuildVarFields(d *config.Deployment, vars []*config.Variable, values map[string]string, savedSecrets map[string]bool, errors map[string]string) []VarFieldVM {
	active := d.ActiveSet(values)
	fields := make([]VarFieldVM, 0, len(vars))
	for _, v := range vars {
		f := VarFieldVM{V: v, Error: errors[v.Name], Active: active[v.Name]}
		if v.Secret {
			f.HasSaved = savedSecrets[v.Name]
		} else {
			f.Value = values[v.Name]
			f.HasSaved = f.Value != ""
		}
		fields = append(fields, f)
	}
	return fields
}

// BuildVarLayout arranges the fields of BuildVarFields into the group/row
// layout the deployment declares.
func BuildVarLayout(d *config.Deployment, fields []VarFieldVM) VarLayoutVM {
	byGroup := map[int][]VarFieldVM{}
	var ungrouped []VarFieldVM
	var groupIDs []int
	for _, f := range fields {
		if f.V.Group == nil {
			ungrouped = append(ungrouped, f)
			continue
		}
		id := *f.V.Group
		if _, seen := byGroup[id]; !seen {
			groupIDs = append(groupIDs, id)
		}
		byGroup[id] = append(byGroup[id], f)
	}
	sort.Ints(groupIDs)

	layout := VarLayoutVM{Ungrouped: buildVarRows(ungrouped)}
	for _, id := range groupIDs {
		g := config.VarGroup{ID: id}
		if def := d.Group(id); def != nil {
			g = *def
		}
		collapsed := g.Collapsed
		required := 0
		for _, f := range byGroup[id] {
			if f.Error != "" {
				collapsed = false // never hide a validation error
			}
			if f.V.NeedsInput() {
				required++
			}
		}
		layout.Groups = append(layout.Groups, VarGroupVM{
			ID:            id,
			Label:         g.EffectiveLabel(),
			Description:   g.Description,
			Collapsed:     collapsed,
			Rows:          buildVarRows(byGroup[id]),
			Count:         len(byGroup[id]),
			RequiredCount: required,
		})
	}
	return layout
}

// buildVarRows orders one group's fields: explicit rows first in ascending
// row ID (fields sharing a row ID side by side, document order within),
// then rowless fields full-width in document order.
func buildVarRows(fields []VarFieldVM) []VarRowVM {
	byRow := map[int][]VarFieldVM{}
	var rowIDs []int
	var loose []VarFieldVM
	for _, f := range fields {
		if f.V.Row == nil {
			loose = append(loose, f)
			continue
		}
		id := *f.V.Row
		if _, seen := byRow[id]; !seen {
			rowIDs = append(rowIDs, id)
		}
		byRow[id] = append(byRow[id], f)
	}
	sort.Ints(rowIDs)

	rows := make([]VarRowVM, 0, len(rowIDs)+len(loose))
	for _, id := range rowIDs {
		rows = append(rows, VarRowVM{Fields: byRow[id]})
	}
	for _, f := range loose {
		rows = append(rows, VarRowVM{Fields: []VarFieldVM{f}, Full: true})
	}
	return rows
}
