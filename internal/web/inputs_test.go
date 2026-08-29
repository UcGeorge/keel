package web

import (
	"strings"
	"testing"

	"github.com/UcGeorge/keel/internal/config"
)

const inputsYAML = `
version: 1
deployments:
  prod:
    environment: {dockerfile: deploy/Dockerfile}
    steps: [{name: Go, run: "true"}]
    variables:
      REGION: {type: select, default: eu, options: [eu, us]}
      TOKEN: {secret: true}
      NOTES: {required: false}
      DEBUG: {type: boolean, required: false}
      ACTION: {type: select, deploy_time: true, default: deploy, options: [deploy, destroy]}
      CONFIRM: {deploy_time: true, when: {var: ACTION, eq: destroy}}
`

func TestSnapshotInputs(t *testing.T) {
	cfg, err := config.Parse([]byte(inputsYAML))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Deployment("prod")
	snap := SnapshotInputs(d, map[string]string{"TOKEN": "s3cret", "REGION": "us", "ACTION": "deploy"})
	got := map[string]RunInputSnapshot{}
	for _, in := range snap {
		got[in.Name] = in
	}
	check := func(name, source, value string, secret bool) {
		t.Helper()
		in := got[name]
		if in.Source != source || in.Value != value || in.Secret != secret {
			t.Errorf("%s = %+v, want source=%s value=%q secret=%v", name, in, source, value, secret)
		}
	}
	check("REGION", InputSaved, "us", false)
	check("TOKEN", InputSaved, "", true) // never the value
	check("NOTES", InputUnset, "", false)
	check("DEBUG", InputDefault, "false", false)
	check("ACTION", InputDeploy, "deploy", false)
	check("CONFIRM", InputInactive, "", false)
	if snap[0].Name != "REGION" || snap[0].Idx != 0 || !got["ACTION"].DeployTime {
		t.Error("order or deploy-time flag wrong")
	}

	vm := BuildRunInputsVM([]RunInputVM{{Name: "ACTION", DeployTime: true, Source: InputDeploy, Value: "deploy"}, {Name: "REGION", Source: InputSaved, Value: "us"}})
	if len(vm.DeployTime) != 1 || len(vm.Config) != 1 || vm.Empty() {
		t.Errorf("BuildRunInputsVM = %+v", vm)
	}
	if !vm.Config[0].Set() || (RunInputVM{Source: InputInactive}).Set() {
		t.Error("Set is wrong")
	}
	if c := (RunInputChip{Name: "X", Value: strings.Repeat("a", 40)}).ChipValue(); len([]rune(c)) != 24 {
		t.Errorf("ChipValue = %q", c)
	}
}
