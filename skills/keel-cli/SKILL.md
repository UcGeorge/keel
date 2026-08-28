---
name: keel-cli
description: Run Keel from the terminal and CI — keel init, validate, dev, deploy (value precedence, --target, --var, --var-file, exit codes), manifest export, installing or updating the keel binary (keel update), embedding it into a repository with make embed, and keel-cloud configuration. Use when scripting Keel deployments, wiring Keel into GitHub Actions or another CI system, debugging a failed keel deploy, or setting up Keel Cloud.
license: MIT
metadata:
  author: keel
  source: https://github.com/UcGeorge/keel
  docs: https://keel-cloud.mintlify.site/reference/cli
---

# Keel CLI

`keel` is a single static binary: the local UI (`keel dev`), the headless
deployer (`keel deploy`), validation, and manifest generation. Everything
that executes deployments needs Docker running. Docs:
https://keel-cloud.mintlify.site/reference/cli.

## Install

```sh
curl -fsSL https://ucgeorge.github.io/keel/install.sh | sh          # macOS, Linux
irm https://ucgeorge.github.io/keel/install.ps1 | iex                # Windows PowerShell
go install github.com/UcGeorge/keel/cmd/keel@latest                  # Go 1.26+
```

`KEEL_VERSION=v0.3.0` pins a release; `KEEL_INSTALL_DIR` changes the
destination. `keel --version` verifies. Upgrade with `keel update` (verifies
the download's SHA-256, swaps the binary in place; `--check` only reports;
`--version vX.Y.Z` pins or downgrades). The CLI checks for a new release in
the background once a day and prints a note after a command;
`KEEL_NO_UPDATE_CHECK=1` disables it.

## Commands

| Command | What it does |
|---|---|
| `keel init [dir]` | Starter `keel.yaml` + `deploy/Dockerfile`; never overwrites |
| `keel validate [dir]` | Parse and validate; lists every problem with its path; exit 1 when invalid |
| `keel dev [dir] [-p 3400] [--host 127.0.0.1]` | Local UI; creates starter files if missing; state in `.keel/dev.db` |
| `keel deploy <deployment> [-t TARGET] [--var NAME=value]… [--var-file FILE]` | Headless run: build image, run steps, stream log, print outputs |
| `keel manifest <deployment> [-o FILE] [-f md\|html] [--var NAME]… [--project NAME]` | Generate the variable manifest |
| `keel skills install [--agent KEY]… [--global]` | Install these Keel skills into AI coding agents' skill directories |
| `keel update [--check] [--version TAG]` | Self-update from the latest GitHub release, checksum-verified |
| `keel --version`, `keel <command> --help` | |

All commands use the current directory unless `[dir]` is given. Exit
codes: `0` success; `1` any error (invalid configuration, missing or
invalid values, Docker unreachable, failed or canceled run) with the
reason on standard error.

## `keel deploy` value precedence

Lowest to highest:

1. `default`s declared in `keel.yaml`
2. the saved values of `--target NAME` (created in `keel dev` **on this
   machine** — reads `.keel/dev.db` with the machine's `dev.key`; not
   usable in CI or on another machine)
3. `--var-file path` — `NAME=value` per line, `#` comments
4. `--var NAME=value` (repeatable)

Values for undeclared variables are dropped, so one var file can serve
several deployments. The merged set is validated exactly as the UI
validates it before anything runs; missing/invalid values are listed and
the command exits 1. Deploy-time variables are passed per run with
`--var`. Without `--target`, `KEEL_TARGET` inside the run is `cli`.
Ctrl+C cancels the run and removes the container.

```console
$ keel deploy aws-production --target client-acme
$ keel deploy aws-production --var-file .secrets/acme.env --var RUN_MODE=plan
```

On success, non-secret outputs print after `✓ Deployment succeeded`;
secret outputs (and outputs containing a secret input's value) print as
`•••`.

## CI

A CI job needs Docker, the `keel` binary, and the repository; values come
from the CI secret store via `--var`.

```yaml
# .github/workflows/deploy.yml
name: Deploy
on:
  workflow_dispatch:
    inputs:
      target: {description: Client to deploy, type: choice, options: [acme, globex]}
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: curl -fsSL https://ucgeorge.github.io/keel/install.sh | sh
      - env:
          AWS_ACCESS_KEY_ID: ${{ secrets[format('{0}_AWS_ACCESS_KEY_ID', inputs.target)] }}
          AWS_SECRET_ACCESS_KEY: ${{ secrets[format('{0}_AWS_SECRET_ACCESS_KEY', inputs.target)] }}
        run: |
          keel deploy aws-production \
            --var "AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID" \
            --var "AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY" \
            --var AWS_REGION=eu-west-1
```

Also worth a CI job: `keel validate` on every pull request, and
`docker build -f deploy/<x>.Dockerfile .` to catch broken tool pins.
For push-triggered deploys with saved per-target values and run history,
Keel Cloud with its GitHub App does this without a CI job.

## Manifest export

```console
$ keel manifest aws-production -o acme-required-values.md
$ keel manifest aws-production -f html -o values.html --project "Acme API"
$ keel manifest aws-production --var AWS_ACCESS_KEY_ID --var AWS_SECRET_ACCESS_KEY
```

Default selection: every variable with a `manifest` block (unless
`include: false`). Any `--var` replaces the selection. The document lists
each variable's label, type, sensitivity, default, deploy-time and
condition notes, *Why it is needed*, *How to get it*, allowed values, and
format constraints — exactly as written in `keel.yaml`.

## State on disk

| Path | Contents |
|---|---|
| `<repo>/.keel/dev.db` | SQLite: targets, encrypted values, runs, logs, outputs (from `keel dev`) |
| `<repo>/.keel/.gitignore` | written by Keel so `dev.db` stays out of git while `.keel/bin` can be committed |
| `dev.key` in the user config dir | AES-256 key for every `dev.db` on the machine: `~/Library/Application Support/keel/`, `$XDG_CONFIG_HOME/keel/` (`~/.config/keel/`), `%AppData%\keel\` |
| `update-check.json` next to `dev.key` | last background update check; safe to delete |

Deleting `dev.key` makes every saved value on the machine unreadable.

## Embedding the CLI into a repository

From a checkout of the Keel source repository, `make embed DIR=../project`
cross-compiles `keel` for macOS/Linux/Windows (amd64 + arm64) into
`project/.keel/bin/` with `SHA256SUMS` and adds a managed block to
`project/Makefile`. Collaborators then run `make keel-dev`,
`make keel-validate`, `make keel-deploy ARGS="production -t client-a"`,
`make keel-manifest ARGS="…"`, `make keel-run ARGS="…"` with nothing
installed. Re-run `make embed` to upgrade; the managed block is replaced
in place.

## Keel Cloud (`keel-cloud`)

The multi-user edition: organizations, roles, many repositories,
PostgreSQL, push-triggered deploys via a GitHub App. Distributed as a
Linux binary and `ghcr.io/ucgeorge/keel-cloud`. Configured only through
environment variables:

| Variable | Default | Notes |
|---|---|---|
| `KEEL_DATABASE_URL` | — | **required** PostgreSQL URL (`DATABASE_URL` read when unset) |
| `KEEL_ADDR` | `:8080` | listen address |
| `KEEL_BASE_URL` | `http://localhost:8080` | external URL; `https://` turns on Secure cookies |
| `KEEL_DATA_DIR` | `./keel-data` | clones, run workspaces, generated key; must be a host path in a container |
| `KEEL_ENCRYPTION_KEY` | generated | 64 hex chars (`openssl rand -hex 32`); back it up |
| `KEEL_GITHUB_APP_ID`, `KEEL_GITHUB_APP_SLUG`, `KEEL_GITHUB_PRIVATE_KEY[_FILE]`, `KEEL_GITHUB_WEBHOOK_SECRET` | — | GitHub App integration |

Host needs PostgreSQL 13+, `git`, and the Docker CLI with a reachable
daemon. `GET /healthz` returns `ok`. Full guide:
https://keel-cloud.mintlify.site/cloud/self-hosting.
