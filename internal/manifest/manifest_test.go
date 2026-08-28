package manifest

import (
	"strings"
	"testing"
	"time"

	"github.com/smart-minds/keel/internal/config"
)

const yml = `
version: 1
deployments:
  prod:
    description: Deploys the API.
    environment: {dockerfile: deploy/Dockerfile}
    steps: [{name: Deploy, run: ./deploy.sh}]
    variables:
      API_KEY:
        label: Service API Key
        secret: true
        description: The key used to call the billing service.
        manifest:
          why: Authenticates Keel against the billing service.
          how: Open the billing dashboard and create a key under Settings.
      REGION:
        type: select
        options: [{value: us, label: United States}, {value: eu}]
        manifest:
          include: true
      REPLICAS:
        type: number
        required: false
        validation: {min: 1, max: 10}
      INTERNAL_FLAG:
        required: false
`

func parseDep(t *testing.T) *config.Deployment {
	t.Helper()
	cfg, err := config.Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Deployment("prod")
}

func TestDefaultSelection(t *testing.T) {
	d := parseDep(t)
	got := DefaultSelection(d)
	if len(got) != 2 || got[0] != "API_KEY" || got[1] != "REGION" {
		t.Fatalf("selection = %v", got)
	}
}

func TestGenerateDefault(t *testing.T) {
	d := parseDep(t)
	doc, err := Generate(d, Options{
		ProjectName: "acme/api",
		TargetName:  "client-a",
		Now:         time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Required values — acme/api",
		"`prod`",
		"client-a",
		"Service API Key",
		"Authenticates Keel against the billing service.",
		"Open the billing dashboard and create a key under Settings.",
		"**Sensitive — share securely**",
		"`us` — United States",
		"`eu`",
		"August 27, 2026",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("manifest missing %q", want)
		}
	}
	if strings.Contains(doc, "INTERNAL_FLAG") {
		t.Error("unselected variable leaked into manifest")
	}
}

func TestGenerateExplicitSelection(t *testing.T) {
	d := parseDep(t)
	doc, err := Generate(d, Options{Select: []string{"REPLICAS"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "between 1 and 10") {
		t.Errorf("missing numeric constraint text:\n%s", doc)
	}
	if strings.Contains(doc, "API_KEY") {
		t.Error("unselected variable leaked into manifest")
	}
}

func TestGenerateErrors(t *testing.T) {
	d := parseDep(t)
	if _, err := Generate(d, Options{Select: []string{"NOPE"}}); err == nil {
		t.Error("expected error for unknown variable")
	}
	if _, err := Generate(d, Options{Select: []string{}}); err == nil {
		t.Error("expected error for empty selection")
	}
}

func TestSortSelection(t *testing.T) {
	d := parseDep(t)
	got := SortSelection(d, []string{"REPLICAS", "API_KEY"})
	if got[0] != "API_KEY" || got[1] != "REPLICAS" {
		t.Fatalf("got %v", got)
	}
}
