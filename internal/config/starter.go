package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// StarterYAML is the keel.yaml created by `keel init`.
const StarterYAML = `# keel.yaml — Keel deployment configuration
# Reference: docs/keel-yaml.md in the Keel repository.
version: 1

deployments:
  # Each top-level key under ` + "`deployments:`" + ` is one deployment, the way a
  # compose file has services. Rename "production" to whatever fits.
  production:
    # The short description appears on deployment cards and in manifests
    # (plain text); long_description is markdown, rendered on the
    # deployment's own page.
    description: Deploy this project to production.
    long_description: |
      Describe here what this deployment does, step by step. **Markdown**
      works: headings, lists, links, code.

    # The environment is the container the deployment steps run in. Put every
    # CLI and tool the steps need into this Dockerfile (cloud CLIs, terraform,
    # kubectl, ...). A deployment can point at its own Dockerfile, or several
    # deployments can share one.
    environment:
      dockerfile: deploy/Dockerfile
      context: .

    # Steps run in order inside the environment, with the repository mounted
    # at /workspace and every variable exported as an environment variable.
    # A step that fails (non-zero exit) stops the run.
    steps:
      - name: Show environment
        run: |
          echo "Deploying $KEEL_DEPLOYMENT to target $KEEL_TARGET"
          # KEEL_TARGET is a label that can be renamed; KEEL_TARGET_ID never changes.
          # Key anything a later run must find again (state, stack names) by the ID.
          echo "Target ID: ${KEEL_TARGET_ID:-none}"
          echo "Greeting: $GREETING"
      - name: Deploy
        run: |
          echo "Put your real deployment commands here."

    # Variables are the inputs a deployment target must provide — the same
    # idea as terraform variables. Each one becomes an environment variable
    # inside the run. The form shown in Keel Dev and Keel Cloud is rendered
    # from these definitions. With many variables, organize the form with
    # collapsible groups and side-by-side rows (group / row / flex plus a
    # deployment-level groups: map). A variable with deploy_time: true is
    # asked for every time a deploy starts, and "when:" makes a variable
    # conditional on another one's value — see docs/keel-yaml.md.
    variables:
      GREETING:
        label: Greeting
        type: text
        required: true
        default: hello
        description: An example variable. Replace it with your real inputs.
        manifest:
          include: true
          why: Demonstrates how a variable appears in a generated manifest.
          how: Any short text works. Delete this variable once you add real ones.
`

// StarterDockerfile is the deploy/Dockerfile created by `keel init`.
const StarterDockerfile = `# deploy/Dockerfile — the Keel deployment environment
#
# This image defines WHERE your deployment steps run. Install every tool the
# steps need: cloud CLIs (awscli, gcloud, az), terraform, kubectl, helm, ...
#
# Example for AWS:
#   FROM amazon/aws-cli:latest
# Example for GCP:
#   FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:slim
#
# The starter uses plain alpine so the sample deployment runs anywhere.
FROM alpine:3.20

RUN apk add --no-cache bash curl git

# Keel mounts your repository at /workspace and runs every step there.
WORKDIR /workspace
`

// InitResult describes what Init created or skipped.
type InitResult struct {
	CreatedConfig     bool
	CreatedDockerfile bool
	ConfigPath        string
	DockerfilePath    string
}

// Init writes the starter keel.yaml and deploy/Dockerfile into dir,
// never overwriting files that already exist.
func Init(dir string) (*InitResult, error) {
	res := &InitResult{
		ConfigPath:     filepath.Join(dir, DefaultFileName),
		DockerfilePath: filepath.Join(dir, "deploy", "Dockerfile"),
	}

	if _, err := Find(dir); err == nil {
		// Config already present; leave it alone.
	} else {
		if err := os.WriteFile(res.ConfigPath, []byte(StarterYAML), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", res.ConfigPath, err)
		}
		res.CreatedConfig = true
	}

	if _, err := os.Stat(res.DockerfilePath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(res.DockerfilePath), 0o755); err != nil {
			return nil, fmt.Errorf("create deploy directory: %w", err)
		}
		if err := os.WriteFile(res.DockerfilePath, []byte(StarterDockerfile), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", res.DockerfilePath, err)
		}
		res.CreatedDockerfile = true
	}
	return res, nil
}
