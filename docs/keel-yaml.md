# keel.yaml reference

`keel.yaml` (or `keel.yml`) lives at the repository root and defines every
deployment of the project. Keel Dev re-reads it on every page load; Keel
Cloud reads it from the connected branch on every sync and push.

```yaml
version: 1          # required; this build supports version 1

deployments:        # required; at least one deployment
  <name>: …         # one entry per deployment
```

Deployment names are lowercase DNS-label style: letters, digits, hyphens
(`aws-production`, `gcp-staging`).

## Deployment

| Key | Required | Description |
|---|---|---|
| `description` | no | Short plain-text summary, shown on deployment cards, in lists, and at the top of generated manifests. |
| `long_description` | no | Full description shown on the deployment's own page. Markdown. |
| `groups` | no | Variable group definitions — see [Layout](#layout-groups-rows-flex). |
| `environment.dockerfile` | **yes** | Path (relative, inside the repo) to the Dockerfile that defines where the steps run. Install every CLI the steps need in this image. |
| `environment.context` | no | Docker build context, relative to the repo root. Default `.`. |
| `steps` | **yes** | Ordered list of `{name, run}`. |
| `variables` | no | Mapping of variable name → variable definition. |
| `outputs` | no | Mapping of output name → output definition — see [Outputs](#outputs). |

Several deployments can point at the same Dockerfile, or each can have its
own (e.g. `deploy/aws.Dockerfile`, `deploy/gcp.Dockerfile`).

### Steps

```yaml
steps:
  - name: Authenticate
    run: |
      gcloud auth activate-service-account --key-file=<(echo "$GCP_KEY_JSON")
  - name: Deploy
    run: ./deploy.sh
```

Each step's `run` is a shell script executed with `/bin/sh` inside the
environment container. The repository checkout is mounted read-write at
`/workspace` (the working directory). A step that exits non-zero fails the
run; later steps are skipped. Each step runs in its own subshell, so `cd`
and shell options don't leak between steps.

Every run also receives `KEEL_DEPLOYMENT`, `KEEL_TARGET`, `KEEL_TARGET_ID`, and
`KEEL_RUN_ID`. Targets can be renamed, so anything that must survive a rename —
a Terraform state key, a stack name — should be keyed by `KEEL_TARGET_ID`
(stable) rather than `KEEL_TARGET`.

**Authentication is just a step.** For clouds where a "sign in with…"
button isn't meaningful for automated deploys, declare the credentials as
variables and write the login as the first step (`aws configure set …`,
`gcloud auth activate-service-account …`, `az login --service-principal …`).

### Variables

Every variable becomes an environment variable inside the run and a form
field in the UI. Names are uppercase env-var style (`AWS_REGION`); the
`KEEL_` prefix is reserved.

```yaml
variables:
  NOTIFY_EMAIL:
    label: Notification email     # form label (default: the name)
    type: email                   # see types below (default: text)
    secret: false                 # store encrypted, render as password, mask in logs
    required: true                # default: true
    description: |                # markdown, shown under the field
      Deployment reports are sent here.
    placeholder: ops@company.com  # form placeholder
    default: ""                   # pre-filled value (not allowed for secrets)
    validation:
      pattern: ".*@company\\.com" # RE2 regex, must match the whole value
      message: Must be a company address.   # error text for the pattern
      min: 1                      # numbers only (inclusive)
      max: 10
      min_length: 3               # text-like types
      max_length: 64
    manifest:                     # entry in the generated variable manifest
      include: true               # default true when a manifest block exists
      title: Notification email   # heading (default: the label)
      why: Where deploy reports go.        # markdown
      how: Ask IT for a distribution list. # markdown
    group: 2                      # layout: variable group (see below)
    row: 1                        # layout: horizontal row within the group
    flex: 2                       # layout: share of the row's width
    deploy_time: true             # ask for this value on every deploy (modal)
    when: {var: ACTION, eq: deploy}   # conditional: active only while this holds
```

#### Deploy-time variables

`deploy_time: true` takes a variable out of the target's stored
configuration: the UI asks for it in a modal **every time the Deploy button
is pressed**, and the value applies to that run only. Everything else about
the variable (type, validation, defaults, layout, manifest) works the same.

- The modal pre-fills defaults; a required deploy-time variable without a
  value blocks the deploy with an inline error.
- Target readiness ("Ready to deploy") considers only configuration
  variables — deploy-time values are never "missing" before a deploy.
- Headless deploys pass them per run: `keel deploy prod --var RUN_MODE=plan`.
- Push-triggered auto-deploys (Keel Cloud) use the defaults; a required
  deploy-time variable without a default blocks auto-deploy with a clear
  message.

#### Conditional variables

`when:` gates a variable on another variable's value. While the condition
does not hold the variable is **inactive**: its form field is disabled (and
re-enabled live as values change), it is never required, and it is not
exported into the run at all — read it as `${NAME:-}` in steps.

```yaml
ACTION:      {type: select, deploy_time: true, default: deploy, options: [deploy, destroy]}
RUN_MODE:    {type: select, deploy_time: true, default: full, options: [full, plan],
              when: {var: ACTION, eq: deploy}}
DESTROY_MODE: {type: select, deploy_time: true, options: [destroy-all, destroy-data],
              when: {var: ACTION, eq: destroy}}
CONFIRM:     {deploy_time: true, when: {var: ACTION, eq: destroy},
              validation: {pattern: destroy, message: Type "destroy" to confirm.}}
```

A condition is `var` plus exactly one operator:

| Operator | Holds when the referenced value… |
|---|---|
| `eq: v` / `ne: v` | equals / does not equal `v` |
| `in: [a, b]` | is one of the listed values |
| `gt` / `gte` / `lt` / `lte: n` | compares numerically against `n` |
| `set: true` / `set: false` | has any value / is empty |

The referenced value is the variable's *effective* value — what it would
resolve to, including its default. Conditions chain: the referenced
variable may itself have a `when:`, and an inactive variable makes every
condition reading it false (so a whole branch deactivates together).
Validation rejects unknown references, self-references, dependency cycles,
comparisons that can never match a select's options, and configuration
variables depending on deploy-time variables (their values only exist while
a deploy starts).

#### Layout: groups, rows, flex

Large variable sets can be organized into collapsible groups and
side-by-side rows; without any layout keys the form is simply one variable
per line in document order.

```yaml
groups:                # on the deployment, next to `variables:`
  1: Credentials       # shorthand: just a label
  2:
    label: Tuning
    description: Rarely needed — the defaults are correct.  # markdown
    collapsed: true    # start collapsed in the UI

variables:
  ACCESS_KEY:  {group: 1, row: 1}
  SECRET_KEY:  {group: 1, row: 1, flex: 2}   # twice as wide as ACCESS_KEY
  REGION:      {group: 1}
  IMAGE_TAG:   {}                            # ungrouped
```

- **`group`** (integer) places a variable in that group. Groups render in
  ascending ID order, **after** all ungrouped variables. A group referenced
  without a `groups:` entry renders with the label `Group <ID>`; a defined
  group must be referenced by at least one variable.
- **`row`** (integer) puts variables sharing a row ID (within the same
  group) side by side, rows in ascending ID order. Variables **without** a
  row render **last** in their group, each taking the full width.
- **`flex`** (positive number, requires `row`) is the variable's share of
  its row's width, CSS flex-grow style — two variables with `flex: 1` and
  `flex: 2` split the row 1:2. Default `1`. Rows wrap on narrow screens.

#### Types

| Type | Form control | Validation |
|---|---|---|
| `text` | text input | pattern / lengths |
| `multiline` | textarea | pattern / lengths |
| `number` | number input | numeric, `min` / `max` |
| `email` | email input | address shape |
| `url` | url input | scheme + host required |
| `boolean` | true/false select | `true` or `false`; unset optional booleans resolve to `false` |
| `select` | dropdown | one of `options` |

`options` entries are either plain strings or `{value, label}`:

```yaml
type: select
options:
  - us-east-1
  - value: eu-west-1
    label: Europe (Ireland)
```

#### Secrets

`secret: true` values are stored encrypted (AES-256-GCM), rendered as
masked fields — multi-line ones too — that show a *saved — leave blank to keep* placeholder once a value exists, never echoed back
into forms, and masked as `•••` wherever they appear in run logs. Secrets
cannot declare defaults, and boolean/select variables cannot be secret.

## Outputs

Outputs are environment variables captured from the container **at the end
of a fully successful run** — any variable that exists in the environment
then, including everything steps `export`ed along the way (exports carry
across steps):

```yaml
steps:
  - name: Deploy
    run: |
      ./deploy.sh
      export SERVICE_URL="$(terraform output -raw service_url)"
      export DB_PASSWORD="$(terraform output -raw db_password)"

outputs:
  SERVICE_URL:
    label: Service URL              # default: the name
    description: Where the API answers.   # markdown
  DB_PASSWORD:
    secret: true                    # hidden behind a reveal control
```

- Captured values show on the **run page** and, for the most recent
  successful run, on the **target page** ("Latest outputs"), each with a
  copy button. `keel deploy` prints them too.
- **Secret outputs** are stored encrypted and render masked (`••••••••`)
  until revealed. In Keel Cloud, only members with the deploy or configure
  scope receive secret values at all. A value that contains a secret input
  is treated as secret automatically, and an output that shares a secret
  variable's name must set `secret: true` — declared or not, a credential
  never renders in the clear by accident.
- A declared output that is not set when the run ends shows as *"not set"*;
  failed runs produce no outputs. Values are per run, so the history keeps
  each run's outputs. Values never pass through the visible log stream.

## The variable manifest

The manifest is the document you hand to whoever must supply the values —
a client, a third party, another team. Generate it from the UI (target page
→ *Export variable manifest*, pick the variables, download Markdown or
HTML) or the CLI:

```console
$ keel manifest aws-production -o required-values.md
$ keel manifest aws-production --var AWS_ACCESS_KEY_ID --var AWS_REGION -f html -o values.html
```

By default it includes every variable with a `manifest:` block; the builder
lets you adjust the selection. Each entry carries the description, **why it
is needed**, and **how to get it**, plus type, sensitivity, allowed values,
and format constraints in plain language.

## Validation

`keel validate` (and the UI, continuously) checks the whole file and
reports every problem at once with precise paths:

```
✗ keel.yaml is invalid:
  - deployments.aws-production.environment.dockerfile: is required (the Dockerfile that defines the deployment environment)
  - deployments.aws-production.variables.region: invalid variable name "region" (use uppercase letters, digits, and underscores, e.g. "AWS_REGION")
```
