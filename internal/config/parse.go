package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Find locates the configuration file in dir. It returns the absolute path,
// or an error satisfying os.IsNotExist semantics via ErrNotFound.
func Find(dir string) (string, error) {
	for _, name := range FileNames {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", &NotFoundError{Dir: dir}
}

// NotFoundError reports that no keel configuration file exists in a directory.
type NotFoundError struct{ Dir string }

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no %s found in %s", DefaultFileName, e.Dir)
}

// LoadDir finds, parses, and validates the configuration in dir.
func LoadDir(dir string) (*Config, error) {
	path, err := Find(dir)
	if err != nil {
		return nil, err
	}
	return LoadFile(path)
}

// LoadFile parses and validates the configuration at path.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// Parse parses and validates a keel.yaml document. The returned error, if
// any, is a *ValidationErrors when the document parsed but failed
// validation, or a plain error for malformed YAML.
func Parse(data []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	cfg := &Config{Raw: data}
	verrs := &ValidationErrors{}

	if root.Kind == 0 || len(root.Content) == 0 {
		verrs.Addf("", "configuration is empty")
		return cfg, verrs
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		verrs.Addf("", "configuration must be a YAML mapping")
		return cfg, verrs
	}

	seen := map[string]bool{}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key, val := doc.Content[i], doc.Content[i+1]
		if seen[key.Value] {
			verrs.Addf(key.Value, "duplicate key")
			continue
		}
		seen[key.Value] = true
		switch key.Value {
		case "version":
			v, err := strconv.Atoi(strings.TrimSpace(val.Value))
			if err != nil {
				verrs.Addf("version", "must be an integer (got %q)", val.Value)
			} else {
				cfg.Version = v
			}
		case "deployments":
			parseDeployments(val, cfg, verrs)
		default:
			verrs.Addf(key.Value, "unknown top-level key (expected: version, deployments)")
		}
	}

	validate(cfg, verrs)
	if verrs.HasErrors() {
		return cfg, verrs
	}
	return cfg, nil
}

func parseDeployments(node *yaml.Node, cfg *Config, verrs *ValidationErrors) {
	if node.Kind != yaml.MappingNode {
		verrs.Addf("deployments", "must be a mapping of deployment name to deployment definition")
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i], node.Content[i+1]
		name := key.Value
		path := "deployments." + name
		if cfg.Deployment(name) != nil {
			verrs.Addf(path, "duplicate deployment name")
			continue
		}
		d := &Deployment{Name: name, Environment: Environment{Context: "."}}
		cfg.Deployments = append(cfg.Deployments, d)
		if val.Kind != yaml.MappingNode {
			verrs.Addf(path, "must be a mapping")
			continue
		}
		for j := 0; j+1 < len(val.Content); j += 2 {
			k, v := val.Content[j], val.Content[j+1]
			switch k.Value {
			case "description":
				d.Description = v.Value
			case "long_description":
				d.LongDescription = v.Value
			case "groups":
				parseGroups(v, d, path, verrs)
			case "environment":
				parseEnvironment(v, d, path, verrs)
			case "steps":
				parseSteps(v, d, path, verrs)
			case "variables":
				parseVariables(v, d, path, verrs)
			case "outputs":
				parseOutputs(v, d, path, verrs)
			default:
				verrs.Addf(path+"."+k.Value, "unknown key (expected: description, long_description, groups, environment, steps, variables, outputs)")
			}
		}
	}
}

func parseGroups(node *yaml.Node, d *Deployment, path string, verrs *ValidationErrors) {
	path += ".groups"
	if node.Kind != yaml.MappingNode {
		verrs.Addf(path, "must be a mapping of group ID (an integer) to a label or group definition")
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i], node.Content[i+1]
		gpath := path + "." + key.Value
		id, err := strconv.Atoi(strings.TrimSpace(key.Value))
		if err != nil {
			verrs.Addf(gpath, "group ID must be an integer (got %q)", key.Value)
			continue
		}
		if d.Group(id) != nil {
			verrs.Addf(gpath, "duplicate group ID")
			continue
		}
		g := VarGroup{ID: id}
		switch val.Kind {
		case yaml.ScalarNode:
			g.Label = val.Value // shorthand: `3: Cloud credentials`
		case yaml.MappingNode:
			for j := 0; j+1 < len(val.Content); j += 2 {
				k, n := val.Content[j], val.Content[j+1]
				switch k.Value {
				case "label":
					g.Label = n.Value
				case "description":
					g.Description = n.Value
				case "collapsed":
					parseBool(n, &g.Collapsed, gpath+".collapsed", verrs)
				default:
					verrs.Addf(gpath+"."+k.Value, "unknown key (expected: label, description, collapsed)")
				}
			}
		default:
			verrs.Addf(gpath, "must be a label string or a mapping with label, description, collapsed")
			continue
		}
		d.Groups = append(d.Groups, g)
	}
}

func parseEnvironment(node *yaml.Node, d *Deployment, path string, verrs *ValidationErrors) {
	path += ".environment"
	if node.Kind != yaml.MappingNode {
		verrs.Addf(path, "must be a mapping")
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		switch k.Value {
		case "dockerfile":
			d.Environment.Dockerfile = v.Value
		case "context":
			d.Environment.Context = v.Value
		default:
			verrs.Addf(path+"."+k.Value, "unknown key (expected: dockerfile, context)")
		}
	}
}

func parseSteps(node *yaml.Node, d *Deployment, path string, verrs *ValidationErrors) {
	path += ".steps"
	if node.Kind != yaml.SequenceNode {
		verrs.Addf(path, "must be a list of steps")
		return
	}
	for i, item := range node.Content {
		spath := fmt.Sprintf("%s[%d]", path, i)
		if item.Kind != yaml.MappingNode {
			verrs.Addf(spath, "must be a mapping with name and run")
			continue
		}
		var s Step
		for j := 0; j+1 < len(item.Content); j += 2 {
			k, v := item.Content[j], item.Content[j+1]
			switch k.Value {
			case "name":
				s.Name = v.Value
			case "run":
				s.Run = v.Value
			default:
				verrs.Addf(spath+"."+k.Value, "unknown key (expected: name, run)")
			}
		}
		d.Steps = append(d.Steps, s)
	}
}

func parseVariables(node *yaml.Node, d *Deployment, path string, verrs *ValidationErrors) {
	path += ".variables"
	if node.Kind != yaml.MappingNode {
		verrs.Addf(path, "must be a mapping of variable name to variable definition")
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i], node.Content[i+1]
		name := key.Value
		vpath := path + "." + name
		if d.Variable(name) != nil {
			verrs.Addf(vpath, "duplicate variable name")
			continue
		}
		v := &Variable{Name: name, Type: VarText, Required: true}
		d.Variables = append(d.Variables, v)
		if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
			continue // bare `NAME:` — all defaults
		}
		if val.Kind != yaml.MappingNode {
			verrs.Addf(vpath, "must be a mapping")
			continue
		}
		for j := 0; j+1 < len(val.Content); j += 2 {
			k, n := val.Content[j], val.Content[j+1]
			switch k.Value {
			case "label":
				v.Label = n.Value
			case "type":
				v.Type = VarType(n.Value)
			case "secret":
				parseBool(n, &v.Secret, vpath+".secret", verrs)
			case "required":
				parseBool(n, &v.Required, vpath+".required", verrs)
			case "description":
				v.Description = n.Value
			case "placeholder":
				v.Placeholder = n.Value
			case "default":
				v.Default = n.Value
			case "options":
				parseOptions(n, v, vpath, verrs)
			case "validation":
				parseValidation(n, v, vpath, verrs)
			case "manifest":
				parseManifest(n, v, vpath, verrs)
			case "group":
				parseIntPtr(n, &v.Group, vpath+".group", verrs)
			case "row":
				parseIntPtr(n, &v.Row, vpath+".row", verrs)
			case "flex":
				parseFloat(n, &v.Flex, vpath+".flex", verrs)
			case "deploy_time":
				parseBool(n, &v.DeployTime, vpath+".deploy_time", verrs)
			case "when":
				parseWhen(n, v, vpath, verrs)
			default:
				verrs.Addf(vpath+"."+k.Value, "unknown key (expected: label, type, secret, required, description, placeholder, default, options, validation, manifest, group, row, flex, deploy_time, when)")
			}
		}
	}
}

func parseOutputs(node *yaml.Node, d *Deployment, path string, verrs *ValidationErrors) {
	path += ".outputs"
	if node.Kind != yaml.MappingNode {
		verrs.Addf(path, "must be a mapping of output name to output definition")
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i], node.Content[i+1]
		name := key.Value
		opath := path + "." + name
		if d.Output(name) != nil {
			verrs.Addf(opath, "duplicate output name")
			continue
		}
		o := &Output{Name: name}
		d.Outputs = append(d.Outputs, o)
		if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
			continue // bare `NAME:` — all defaults
		}
		if val.Kind != yaml.MappingNode {
			verrs.Addf(opath, "must be a mapping")
			continue
		}
		for j := 0; j+1 < len(val.Content); j += 2 {
			k, n := val.Content[j], val.Content[j+1]
			switch k.Value {
			case "label":
				o.Label = n.Value
			case "description":
				o.Description = n.Value
			case "secret":
				parseBool(n, &o.Secret, opath+".secret", verrs)
			default:
				verrs.Addf(opath+"."+k.Value, "unknown key (expected: label, description, secret)")
			}
		}
	}
}

func parseOptions(node *yaml.Node, v *Variable, vpath string, verrs *ValidationErrors) {
	opath := vpath + ".options"
	if node.Kind != yaml.SequenceNode {
		verrs.Addf(opath, "must be a list")
		return
	}
	for i, item := range node.Content {
		ipath := fmt.Sprintf("%s[%d]", opath, i)
		switch item.Kind {
		case yaml.ScalarNode:
			v.Options = append(v.Options, Option{Value: item.Value})
		case yaml.MappingNode:
			var o Option
			for j := 0; j+1 < len(item.Content); j += 2 {
				k, n := item.Content[j], item.Content[j+1]
				switch k.Value {
				case "value":
					o.Value = n.Value
				case "label":
					o.Label = n.Value
				default:
					verrs.Addf(ipath+"."+k.Value, "unknown key (expected: value, label)")
				}
			}
			v.Options = append(v.Options, o)
		default:
			verrs.Addf(ipath, "must be a string or a mapping with value and label")
		}
	}
}

func parseValidation(node *yaml.Node, v *Variable, vpath string, verrs *ValidationErrors) {
	path := vpath + ".validation"
	if node.Kind != yaml.MappingNode {
		verrs.Addf(path, "must be a mapping")
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, n := node.Content[i], node.Content[i+1]
		switch k.Value {
		case "pattern":
			v.Validation.Pattern = n.Value
		case "message":
			v.Validation.Message = n.Value
		case "min":
			parseFloatPtr(n, &v.Validation.Min, path+".min", verrs)
		case "max":
			parseFloatPtr(n, &v.Validation.Max, path+".max", verrs)
		case "min_length":
			parseIntPtr(n, &v.Validation.MinLength, path+".min_length", verrs)
		case "max_length":
			parseIntPtr(n, &v.Validation.MaxLength, path+".max_length", verrs)
		default:
			verrs.Addf(path+"."+k.Value, "unknown key (expected: pattern, message, min, max, min_length, max_length)")
		}
	}
}

func parseManifest(node *yaml.Node, v *Variable, vpath string, verrs *ValidationErrors) {
	path := vpath + ".manifest"
	if node.Kind != yaml.MappingNode {
		verrs.Addf(path, "must be a mapping")
		return
	}
	// Presence of a manifest block means "include by default" unless
	// explicitly disabled.
	v.Manifest.Include = true
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, n := node.Content[i], node.Content[i+1]
		switch k.Value {
		case "include":
			parseBool(n, &v.Manifest.Include, path+".include", verrs)
		case "title":
			v.Manifest.Title = n.Value
		case "why":
			v.Manifest.Why = n.Value
		case "how":
			v.Manifest.How = n.Value
		default:
			verrs.Addf(path+"."+k.Value, "unknown key (expected: include, title, why, how)")
		}
	}
}

func parseWhen(node *yaml.Node, v *Variable, vpath string, verrs *ValidationErrors) {
	path := vpath + ".when"
	if node.Kind != yaml.MappingNode {
		verrs.Addf(path, "must be a mapping with var and one operator (eq, ne, in, gt, gte, lt, lte, set)")
		return
	}
	c := &Condition{}
	ops := 0
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, n := node.Content[i], node.Content[i+1]
		switch k.Value {
		case "var":
			c.Var = n.Value
		case "eq", "ne", "gt", "gte", "lt", "lte":
			c.Op = k.Value
			c.Value = n.Value
			ops++
		case "in":
			c.Op = CondIn
			ops++
			if n.Kind != yaml.SequenceNode {
				verrs.Addf(path+".in", "must be a list of values")
				continue
			}
			for _, item := range n.Content {
				c.Values = append(c.Values, item.Value)
			}
		case "set":
			c.Op = CondSet
			ops++
			parseBool(n, &c.Want, path+".set", verrs)
		default:
			verrs.Addf(path+"."+k.Value, "unknown key (expected: var, eq, ne, in, gt, gte, lt, lte, set)")
		}
	}
	if c.Var == "" {
		verrs.Addf(path+".var", "is required (the variable the condition reads)")
	}
	if ops == 0 {
		verrs.Addf(path, "needs an operator: eq, ne, in, gt, gte, lt, lte, or set")
	} else if ops > 1 {
		verrs.Addf(path, "only one operator is allowed")
	}
	v.When = c
}

func parseBool(n *yaml.Node, dst *bool, path string, verrs *ValidationErrors) {
	switch strings.ToLower(strings.TrimSpace(n.Value)) {
	case "true":
		*dst = true
	case "false":
		*dst = false
	default:
		verrs.Addf(path, "must be true or false (got %q)", n.Value)
	}
}

func parseFloat(n *yaml.Node, dst *float64, path string, verrs *ValidationErrors) {
	f, err := strconv.ParseFloat(strings.TrimSpace(n.Value), 64)
	if err != nil {
		verrs.Addf(path, "must be a number (got %q)", n.Value)
		return
	}
	*dst = f
}

func parseFloatPtr(n *yaml.Node, dst **float64, path string, verrs *ValidationErrors) {
	f, err := strconv.ParseFloat(strings.TrimSpace(n.Value), 64)
	if err != nil {
		verrs.Addf(path, "must be a number (got %q)", n.Value)
		return
	}
	*dst = &f
}

func parseIntPtr(n *yaml.Node, dst **int, path string, verrs *ValidationErrors) {
	i, err := strconv.Atoi(strings.TrimSpace(n.Value))
	if err != nil {
		verrs.Addf(path, "must be an integer (got %q)", n.Value)
		return
	}
	*dst = &i
}
