# Documentation project instructions

This directory is the Keel documentation site, built on
[Mintlify](https://mintlify.com). Pages are MDX with YAML frontmatter;
`docs.json` holds navigation and branding.

## About Keel

Keel turns "how we deploy" into a `keel.yaml` anyone can run through a web
UI. Two editions share one engine: **Keel Dev** (`keel dev`, one repository
on your machine, SQLite) and **Keel Cloud** (`keel-cloud`, many repositories,
organizations, PostgreSQL). The source of truth for behavior is the Go code
in this repository under `cmd/` and `internal/`.

## Terminology

- **deployment** — an entry under `deployments:` in `keel.yaml`; not "pipeline" or "workflow".
- **environment** — the Dockerfile-defined image the steps run in; not "runner".
- **step** — one `{name, run}` entry; not "job" or "task".
- **variable** — a declared input; **value** — what a target stores for it.
- **target** — a concrete place a deployment goes (a client, an environment, a region).
- **run** — one execution of a deployment against a target; "deploy" is the verb.
- **output** — an environment variable captured at the end of a successful run.
- **variable manifest** — the generated document listing the values someone must supply.
- **Keel Dev** / **Keel Cloud** — the two editions; the CLI binaries are `keel` and `keel-cloud`.
- Write `keel.yaml` in code formatting; write "Keel" (capitalized) for the product.

## Style

- Second person, active voice, sentence-case headings.
- Bold for UI elements the reader clicks: **Deploy now**, **Export variable manifest**.
- Code formatting for file names, commands, paths, variable names, and keys.
- Prefer one realistic example over several abstract ones. Use names like
  `aws-production`, `client-acme`, `AWS_REGION` — never `foo`.
- State limits and defaults with their exact values (port `3400`, 64 KiB
  output cap, 14-day invites) and keep them in sync with the code.

## Content boundaries

- Document behavior that exists in the code. Mark anything you cannot
  verify with `{/* TODO: … */}` rather than guessing.
- Do not document internal Go packages, database schemas, or template
  internals — the docs are for people who use Keel, not for contributors.
