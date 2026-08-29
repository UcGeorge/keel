# Keel

**Deployments your whole team can run.**

Every project has one person who really knows how to deploy it — the steps,
the CLIs, the variables, the section of a README that says *"then export
these six things and run this script"*. Keel turns that tribal knowledge
into a `keel.yaml` that anyone — including non-technical, client-facing
operators — can run through a clean web UI.

- **Deployments** are defined in `keel.yaml`, the way services are defined
  in a compose file. Each deployment names its **environment** (a Dockerfile
  with every tool the steps need), its ordered **steps**, and its
  **variables**.
- **Variables** are declared like terraform inputs — typed, validated,
  optionally secret — and Keel renders a proper form for them: email fields,
  dropdowns, numbers with ranges, custom regex validation, help text. Large
  variable sets stay manageable with collapsible **groups** and
  side-by-side **rows** (`group`, `row`, `flex`) declared right on the
  variables. **Deploy-time variables** (`deploy_time: true`) are asked for
  in a modal every time the Deploy button is pressed, and **conditional
  variables** (`when: {var: ACTION, eq: destroy}`) activate only while
  another variable's value satisfies a condition — chains included.
- **Targets** are concrete places a deployment goes — a client, an
  environment, a region. Each target keeps its own variable values
  (encrypted at rest) and its own run history.
- **Runs** execute the steps inside the environment container with the
  repository mounted and every variable exported, with live logs, per-step
  status, and cancellation. Secrets are masked in the logs.
- **Outputs** are environment variables captured when a run succeeds — a
  service URL, an image reference, a rotated key. Declare them under
  `outputs:`, `export` them from any step, and they appear on the run page
  and the target's "Latest outputs" with copy buttons — secret ones stored
  encrypted and hidden behind a reveal control.
- **Variable manifests** are generated documents you hand to whoever must
  supply the values — each entry explains what the value is, why it is
  needed, and how to obtain it, exactly as the deployment author wrote it.
- **Run inputs** are recorded with every run: the deploy-time choices
  (`ACTION=destroy`, an image tag) and the target's configuration at that
  moment, so a run stays explainable a week later. Secret inputs are
  recorded as *set*, never as values.

Keel ships in two forms sharing the same engine and the same UI — with a
light and a dark theme (the header toggle remembers your choice; the
default follows the system preference):

| | **Keel Dev** (`keel dev`) | **Keel Cloud** (`keel-cloud`) |
|---|---|---|
| Where | your machine, one repository | your server, many repositories |
| State | `.keel/` in the repo (SQLite) | PostgreSQL |
| Users | just you | organizations, roles, scopes, invites |
| Repos | the directory you run it in | connected via git URL or GitHub App |
| CI | — | push-triggered auto-deploys via GitHub App webhooks |

## Documentation

- **Docs:** https://keel-cloud.mintlify.site — install, quickstart, concepts,
  guides, and the complete `keel.yaml`, CLI, and Keel Cloud reference.
- **Site:** https://ucgeorge.github.io/keel/
- **Source of the docs:** [`docs/mintlify/`](docs/mintlify/) (Mintlify).

## Install

macOS and Linux:

```console
$ curl -fsSL https://ucgeorge.github.io/keel/install.sh | sh
```

Windows (PowerShell): `irm https://ucgeorge.github.io/keel/install.ps1 | iex`.
With Go: `go install github.com/UcGeorge/keel/cmd/keel@latest`. Archives
and checksums for every platform are on the
[releases page](https://github.com/UcGeorge/keel/releases); the install
scripts verify the SHA-256 of what they download.

Upgrade with `keel update` — it verifies and swaps the binary in place. The
CLI also checks for a new release in the background once a day and says so
after a command (`KEEL_NO_UPDATE_CHECK=1` turns that off).

## Quick start (local)

Requirements: Docker running.

```console
$ keel init          # starter keel.yaml + deploy/Dockerfile
$ keel dev           # UI at http://127.0.0.1:3400
```

Edit `keel.yaml`, refresh the page — the config is re-read on every load.
Create a target, fill in its variables, hit **Deploy now**.

The CLI can do everything headlessly too:

```console
$ keel validate
$ keel deploy production --target client-acme          # saved values
$ keel deploy production --var GREETING=hi             # ad-hoc values
$ keel manifest production -o required-values.md       # variable manifest
```

## AI coding agents

Keel ships agent skills — `SKILL.md` documents that teach Claude Code,
Codex, Cursor, Gemini CLI, GitHub Copilot, OpenCode, Windsurf, and other
agents the `keel.yaml` schema, the validation rules, environment-image
patterns, and the CLI — so an agent asked to "make this project deployable
with Keel" gets it right. Install them into the agents detected on your
machine (project-level, so they can be committed) or user-wide:

```console
$ keel skills install            # .claude/skills, .agents/skills, … in this repo
$ keel skills install --global   # ~/.claude/skills, ~/.codex/skills, …
$ keel skills agents             # supported agents and their directories
```

The skills live in [`skills/`](skills/); see the
[guide](https://keel-cloud.mintlify.site/guides/ai-agents).

## Quick start (cloud)

```console
$ docker compose up --build
```

Open http://localhost:8080, create the first account (you get a personal
organization), connect a repository by git URL (plus a read token if it's
private), and deploy. See [docs/keel-cloud.md](docs/keel-cloud.md) for
production setup, the GitHub App integration, and the permission model.

For a real installation, **Keel deploys itself**: this repository's own
[`keel.yaml`](keel.yaml) provisions a production Keel Cloud on a single
GCE VM (caddy TLS + keel-cloud + PostgreSQL, Cloud Build images, nightly
GCS backups, IAP-only SSH) for ~$35/month — sized for one company and a
few of its clients. Run `keel dev` here, fill in the `cloud-gcp` target,
press Deploy. Architecture and runbook:
[docs/deploy-keel-cloud.md](docs/deploy-keel-cloud.md).

## The configuration

```yaml
version: 1

deployments:
  aws-production:
    description: Deploy the API to AWS ECS.

    environment:
      dockerfile: deploy/aws.Dockerfile   # tools the steps need live here
      context: .

    steps:
      - name: Authenticate
        run: ./deploy/aws-login.sh
      - name: Deploy
        run: ./deploy/release.sh

    variables:
      AWS_ACCESS_KEY_ID:
        label: AWS Access Key ID
        secret: true
        validation: {pattern: "AKIA[0-9A-Z]{16}"}
        manifest:
          why: Authenticates the deployment with your AWS account.
          how: Create an IAM user with programmatic access and attach the deploy policy.
      AWS_REGION:
        type: select
        default: us-east-1
        options: [us-east-1, {value: eu-west-1, label: Europe (Ireland)}]
```

A project can define many deployments (GCP, Azure, staging, …), each with
its own Dockerfile — or several deployments can share one. Full reference:
[docs/keel-yaml.md](docs/keel-yaml.md).

## Repository layout

```
cmd/keel            the CLI (init, validate, dev, deploy, manifest, skills, update)
cmd/keel-cloud      the cloud web service
internal/config     keel.yaml schema, parser, validation
internal/engine     Docker execution engine (build → run steps, log streaming)
internal/manifest   variable manifest generation
internal/web        shared templates (html/template + HTMX + Tailwind), view models
internal/devserver  keel dev UI
internal/cloudserver Keel Cloud (orgs, auth, repos, webhooks)
internal/store      sqlc queries + golang-migrate migrations (SQLite & PostgreSQL)
internal/agentskills agent skill directories + installer behind `keel skills`
internal/selfupdate  release lookup, background update check, `keel update`
skills/             the agent skills (SKILL.md per skill), embedded into the CLI
```

## Embedding Keel into a repository

Teams without this source tree can still get the full local Keel experience.
From the Keel repository:

```console
$ make embed DIR=../your-project
```

This cross-compiles the `keel` CLI for macOS, Linux, and Windows
(amd64 + arm64) into `your-project/.keel/bin/` (with `SHA256SUMS`), and adds
a managed block of targets to `your-project/Makefile`. Once those are
committed, anyone who clones that project can run:

```console
$ make keel              # CLI help
$ make keel-dev          # the Keel UI for that repository
$ make keel-validate     # validate its keel.yaml
$ make keel-deploy ARGS="production -t client-a"
$ make keel-manifest ARGS="production -o values.md"
$ make keel-run ARGS="…" # any other keel command
```

The targets pick the right binary for the host OS/architecture
automatically. Re-running `make embed` rebuilds the binaries and replaces
the managed Makefile block in place — that's also the upgrade path.
`.keel/.gitignore` keeps Keel's machine-local state (like `dev.db`) out of
version control while leaving `bin/` committable.

## Development

```console
$ make build        # bin/keel + bin/keel-cloud
$ make test-short   # fast tests
$ make test         # full suite: Docker engine tests + cloud E2E (Docker + disposable PostgreSQL)
$ make css          # recompile Tailwind after editing templates (npm install once)
$ make sqlc         # regenerate query code after editing queries.sql / migrations
$ make docs         # preview the documentation site (npm i -g mint once)
$ make site         # preview the landing page
```

### Releasing

Push a tag and the `Release` workflow does the rest — GoReleaser builds
`keel` for macOS/Linux/Windows and `keel-cloud` for Linux, publishes the
archives and `checksums.txt` to a GitHub release, and the `keel-cloud`
image goes to `ghcr.io/ucgeorge/keel-cloud`:

```console
$ git tag v0.1.0 && git push origin v0.1.0
```

The landing page and install scripts publish to GitHub Pages from `site/`
and `scripts/` on every push to `main` (`Pages` workflow; enable
*Settings → Pages → Source: GitHub Actions* once).

The full suite ends with a real end-to-end test: it boots PostgreSQL in a
container, serves a git repository over HTTP, signs up, connects the repo,
configures a target, runs a real Docker deployment, and verifies logs,
secret masking, manifests, and the permission model.
