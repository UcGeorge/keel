package config

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// validate performs semantic validation of a parsed document, recording
// every problem it finds into verrs.
func validate(cfg *Config, verrs *ValidationErrors) {
	if cfg.Version == 0 {
		verrs.Addf("version", "is required (add `version: %d`)", SupportedVersion)
	} else if cfg.Version != SupportedVersion {
		verrs.Addf("version", "unsupported version %d (this build of Keel supports version %d)", cfg.Version, SupportedVersion)
	}
	if len(cfg.Deployments) == 0 {
		verrs.Addf("deployments", "at least one deployment is required")
	}
	for _, d := range cfg.Deployments {
		validateDeployment(d, verrs)
	}
}

func validateDeployment(d *Deployment, verrs *ValidationErrors) {
	p := "deployments." + d.Name
	if !ValidDeploymentName(d.Name) {
		verrs.Addf(p, "invalid deployment name %q (use lowercase letters, digits, and hyphens, e.g. \"aws-production\")", d.Name)
	}
	if strings.TrimSpace(d.Environment.Dockerfile) == "" {
		verrs.Addf(p+".environment.dockerfile", "is required (the Dockerfile that defines the deployment environment)")
	} else if !validRelPath(d.Environment.Dockerfile) {
		verrs.Addf(p+".environment.dockerfile", "must be a relative path inside the repository (got %q)", d.Environment.Dockerfile)
	}
	if d.Environment.Context == "" {
		d.Environment.Context = "."
	}
	if !validRelPath(d.Environment.Context) {
		verrs.Addf(p+".environment.context", "must be a relative path inside the repository (got %q)", d.Environment.Context)
	}
	if len(d.Steps) == 0 {
		verrs.Addf(p+".steps", "at least one step is required")
	}
	for i, s := range d.Steps {
		sp := p + ".steps[" + itoa(i) + "]"
		if strings.TrimSpace(s.Name) == "" {
			verrs.Addf(sp+".name", "is required")
		}
		if strings.TrimSpace(s.Run) == "" {
			verrs.Addf(sp+".run", "is required")
		}
	}
	for _, v := range d.Variables {
		validateVariable(v, p, verrs)
	}
	validateConditions(d, p, verrs)
	for _, o := range d.Outputs {
		op := p + ".outputs." + o.Name
		if !varNameRe.MatchString(o.Name) {
			verrs.Addf(op, "invalid output name %q (use uppercase letters, digits, and underscores, e.g. \"SERVICE_URL\")", o.Name)
		} else if strings.HasPrefix(o.Name, ReservedVarPrefix) {
			verrs.Addf(op, "output names may not start with %q (reserved for Keel metadata)", ReservedVarPrefix)
		}
		// An output sharing a secret input's name carries that secret's
		// value — refusing to display it in the clear must be explicit.
		if v := d.Variable(o.Name); v != nil && v.Secret && !o.Secret {
			verrs.Addf(op+".secret", "%q is a secret variable, so this output must set secret: true", o.Name)
		}
	}
	for _, g := range d.Groups {
		used := false
		for _, v := range d.Variables {
			if v.Group != nil && *v.Group == g.ID {
				used = true
				break
			}
		}
		if !used {
			verrs.Addf(p+".groups."+itoa(g.ID), "group is not referenced by any variable")
		}
	}
}

func validateVariable(v *Variable, dpath string, verrs *ValidationErrors) {
	p := dpath + ".variables." + v.Name
	if !varNameRe.MatchString(v.Name) {
		verrs.Addf(p, "invalid variable name %q (use uppercase letters, digits, and underscores, e.g. \"AWS_REGION\")", v.Name)
	} else if strings.HasPrefix(v.Name, ReservedVarPrefix) {
		verrs.Addf(p, "variable names may not start with %q (reserved for Keel metadata)", ReservedVarPrefix)
	}

	typeOK := false
	for _, t := range VarTypes {
		if v.Type == t {
			typeOK = true
			break
		}
	}
	if !typeOK {
		verrs.Addf(p+".type", "unknown type %q (expected one of: %s)", v.Type, joinTypes())
	}

	if v.Type == VarSelect && len(v.Options) == 0 {
		verrs.Addf(p+".options", "type \"select\" requires at least one option")
	}
	if v.Type != VarSelect && len(v.Options) > 0 {
		verrs.Addf(p+".options", "options are only allowed for type \"select\"")
	}
	seen := map[string]bool{}
	for i, o := range v.Options {
		op := p + ".options[" + itoa(i) + "]"
		if strings.TrimSpace(o.Value) == "" {
			verrs.Addf(op, "option value is required")
		}
		if seen[o.Value] {
			verrs.Addf(op, "duplicate option value %q", o.Value)
		}
		seen[o.Value] = true
	}

	if v.Secret {
		if v.Default != "" {
			verrs.Addf(p+".default", "secrets may not declare a default value")
		}
		if v.Type == VarBoolean || v.Type == VarSelect {
			verrs.Addf(p+".secret", "type %q cannot be a secret", v.Type)
		}
	}

	val := &v.Validation
	if val.Pattern != "" {
		re, err := regexp.Compile("^(?:" + val.Pattern + ")$")
		if err != nil {
			verrs.Addf(p+".validation.pattern", "invalid regular expression: %v", err)
		} else {
			val.compiled = re
		}
	}
	if val.Min != nil && val.Max != nil && *val.Min > *val.Max {
		verrs.Addf(p+".validation", "min (%v) is greater than max (%v)", *val.Min, *val.Max)
	}
	if val.MinLength != nil && *val.MinLength < 0 {
		verrs.Addf(p+".validation.min_length", "must be >= 0")
	}
	if val.MinLength != nil && val.MaxLength != nil && *val.MinLength > *val.MaxLength {
		verrs.Addf(p+".validation", "min_length (%d) is greater than max_length (%d)", *val.MinLength, *val.MaxLength)
	}
	if (val.Min != nil || val.Max != nil) && v.Type != VarNumber {
		verrs.Addf(p+".validation", "min/max are only allowed for type \"number\"")
	}
	if (val.MinLength != nil || val.MaxLength != nil) && !isTextual(v.Type) {
		verrs.Addf(p+".validation", "min_length/max_length are only allowed for text-like types")
	}
	if val.Pattern != "" && (v.Type == VarBoolean || v.Type == VarSelect) {
		verrs.Addf(p+".validation.pattern", "pattern is not allowed for type %q", v.Type)
	}

	if v.Flex != 0 {
		if v.Flex < 0 {
			verrs.Addf(p+".flex", "must be a positive number")
		}
		if v.Row == nil {
			verrs.Addf(p+".flex", "flex requires a row (flex splits a row's width between its variables)")
		}
	}

	// A declared default must itself satisfy the variable's constraints.
	if v.Default != "" {
		if msg := CheckValue(v, v.Default); msg != "" {
			verrs.Addf(p+".default", "default value is invalid: %s", msg)
		}
	}
}

// validateConditions checks every `when:` clause against the deployment's
// other variables and rejects dependency cycles.
func validateConditions(d *Deployment, dpath string, verrs *ValidationErrors) {
	for _, v := range d.Variables {
		if v.When == nil {
			continue
		}
		p := dpath + ".variables." + v.Name + ".when"
		c := v.When
		if c.Var == v.Name {
			verrs.Addf(p+".var", "a variable cannot depend on itself")
			continue
		}
		ref := d.Variable(c.Var)
		if c.Var != "" && ref == nil {
			verrs.Addf(p+".var", "unknown variable %q", c.Var)
			continue
		}
		if ref != nil && ref.DeployTime && !v.DeployTime {
			verrs.Addf(p+".var", "a configuration variable cannot depend on deploy-time variable %q (its value only exists while a deploy starts) — mark %s as deploy_time too", c.Var, v.Name)
		}
		switch c.Op {
		case CondGt, CondGte, CondLt, CondLte:
			if _, err := strconv.ParseFloat(strings.TrimSpace(c.Value), 64); err != nil {
				verrs.Addf(p+"."+c.Op, "must be a number to compare against (got %q)", c.Value)
			}
		case CondIn:
			if len(c.Values) == 0 {
				verrs.Addf(p+".in", "needs at least one value")
			}
		}
		// Catch comparisons that can never hold against a select's options.
		if ref != nil && ref.Type == VarSelect && (c.Op == CondEq || c.Op == CondIn) {
			operands := c.Values
			if c.Op == CondEq {
				operands = []string{c.Value}
			}
			for _, want := range operands {
				found := false
				for _, o := range ref.Options {
					if o.Value == want {
						found = true
						break
					}
				}
				if !found {
					verrs.Addf(p+"."+c.Op, "%q is not one of %s's options", want, c.Var)
				}
			}
		}
	}

	// Reject cycles in the dependency graph (A when B, B when A). Without
	// this, every member of a cycle would silently evaluate inactive.
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := map[string]int{}
	var visit func(v *Variable) bool // true = cycle found through v
	visit = func(v *Variable) bool {
		switch state[v.Name] {
		case visiting:
			return true
		case done:
			return false
		}
		state[v.Name] = visiting
		cycle := false
		if v.When != nil {
			if ref := d.Variable(v.When.Var); ref != nil && ref != v {
				cycle = visit(ref)
			}
		}
		state[v.Name] = done
		return cycle
	}
	for _, v := range d.Variables {
		if state[v.Name] == unvisited && v.When != nil && visit(v) {
			verrs.Addf(dpath+".variables."+v.Name+".when", "condition dependencies form a cycle through %q", v.When.Var)
		}
	}
}

func isTextual(t VarType) bool {
	switch t {
	case VarText, VarMultiline, VarEmail, VarURL:
		return true
	}
	return false
}

func joinTypes() string {
	parts := make([]string, len(VarTypes))
	for i, t := range VarTypes {
		parts[i] = string(t)
	}
	return strings.Join(parts, ", ")
}

// validRelPath reports whether p is a clean relative path that stays inside
// the repository root.
func validRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	clean := path.Clean(p)
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

func itoa(i int) string { return strconv.Itoa(i) }
