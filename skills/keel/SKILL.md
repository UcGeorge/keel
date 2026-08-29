---
name: keel
description: Author and maintain keel.yaml deployment configurations for Keel — deployments, environment Dockerfiles, steps, typed variables, form layout, deploy-time and conditional variables, outputs, and variable manifests. Use when creating or editing keel.yaml, adding a deployment or variable, fixing `keel validate` errors, or whenever a user mentions Keel, keel.yaml, deployment targets, or variable manifests.
license: MIT
metadata:
  author: keel
  source: https://github.com/UcGeorge/keel
  docs: https://keel-cloud.mintlify.site
---

# Keel: author `keel.yaml`

Keel turns "how we deploy" into a `keel.yaml` that anyone — including
non-technical operators — can run through a web UI (`keel dev` locally,
Keel Cloud on a server) or headlessly (`keel deploy`). Each **deployment**
names an **environment** (a Dockerfile with every tool the steps need), its
ordered **steps** (shell run inside that container with the repository
mounted), and its **variables** (typed inputs rendered as a form and
exported as environment variables). A **target** is one concrete place a
deployment goes (a client, an environment, a region) with its own saved
values; a **run** is one execution; **outputs** are environment variables
captured when a run succeeds; a **variable manifest** is a generated
document telling someone which values to supply and how to obtain them.

Full documentation: https://keel-cloud.mintlify.site — prefer it over
anything you remember about Keel. The complete key-by-key schema is in
[references/keel-yaml.md](references/keel-yaml.md); worked patterns are in
[references/patterns.md](references/patterns.md).

## Workflow

1. **Find the configuration.** `keel.yaml` (or `keel.yml`) lives at the
   repository root. If there is none, run `keel init` (creates a starter
   `keel.yaml` and `deploy/Dockerfile`, never overwrites) or write the file
   from the skeleton below.
2. **Read what deploys today** before writing: the deploy scripts, the
   README section that lists the CLIs and environment variables, CI jobs.
   Every tool goes into the environment Dockerfile; every command becomes a
   step; every input becomes a variable.
3. **Write or edit `keel.yaml`.** Keep steps short and put logic in scripts
   committed to the repository. Declare one variable per input with a type,
   mark secrets, and write `manifest.why` / `manifest.how` for every value
   an operator must supply.
4. **Validate:** `keel validate` reports every problem with its path. Fix
   until it prints `✓ keel.yaml is valid`.
5. **Try it:** `keel dev` (UI, needs Docker) or
   `keel deploy <deployment> --var NAME=value` (headless). Iterate on the
   steps until the run is green.

Terminology: say *deployment* (not pipeline/workflow), *environment* (not
runner), *step* (not job/task), *variable* / *value*, *target*, *run*,
*output*, *variable manifest*.

## Skeleton

```yaml
version: 1                       # required, must be 1

deployments:
  aws-production:                # lowercase DNS-label style, unique
    description: Deploy the API to AWS ECS.          # plain text, one line
    long_description: |                              # Markdown, optional
      Builds and pushes the image, then updates the ECS service.

    environment:
      dockerfile: deploy/aws.Dockerfile   # required; relative, inside the repo
      context: .                          # default "."

    steps:                                # at least one; run in order with /bin/sh
      - name: Configure AWS credentials
        run: |
          aws configure set aws_access_key_id "$AWS_ACCESS_KEY_ID"
          aws configure set aws_secret_access_key "$AWS_SECRET_ACCESS_KEY"
          aws configure set region "$AWS_REGION"
          aws sts get-caller-identity
      - name: Update service
        run: |
          ./deploy/aws/update-service.sh
          export SERVICE_URL="$(./deploy/aws/service-url.sh)"

    variables:
      AWS_ACCESS_KEY_ID:
        label: AWS Access Key ID
        secret: true
        validation:
          pattern: "AKIA[0-9A-Z]{16}"          # RE2, matches the whole value
          message: Must look like AKIAXXXXXXXXXXXXXXXX
        manifest:
          why: Authenticates the deployment with your AWS account.
          how: "IAM → Users → Add user, enable programmatic access, copy the key ID."
      AWS_SECRET_ACCESS_KEY:
        label: AWS Secret Access Key
        secret: true
        manifest:
          why: The second half of the AWS credential pair.
          how: Shown once when the access key is created.
      AWS_REGION:
        label: Region
        type: select
        default: us-east-1
        options:
          - us-east-1
          - {value: eu-west-1, label: Europe (Ireland)}
      DESIRED_COUNT:
        label: Service replicas
        type: number
        required: false
        default: "2"                          # defaults are always strings
        validation: {min: 1, max: 20}

    outputs:
      SERVICE_URL:
        label: Service URL
        description: The load balancer address the API answers on.
```

## Rules validation enforces (the ones people trip on)

- **Unknown keys anywhere are errors** — a typo never silently does
  nothing. Only `version` and `deployments` at the top level.
- Deployment names: `[a-z0-9]([a-z0-9-]*[a-z0-9])?`, ≤ 63 chars, unique.
- `environment.dockerfile` is required and must be a relative path inside
  the repository (no `..`, no absolute paths). Same for `context`.
- At least one step; every step needs a non-empty `name` and `run`.
- Variable and output names match `[A-Z][A-Z0-9_]*`, are unique, and may
  not start with `KEEL_` (reserved for `KEEL_DEPLOYMENT`, `KEEL_TARGET`, `KEEL_TARGET_ID`,
  `KEEL_RUN_ID`).
- `type` is one of `text` (default), `multiline`, `number`, `email`, `url`,
  `boolean`, `select`.
- `select` requires `options` (non-empty, unique values); other types must
  not have `options`.
- Secrets (`secret: true`) cannot have a `default` and cannot be `boolean`
  or `select`.
- `default` is a string (`"2"`, `"true"`) and must itself pass the
  variable's validation.
- `validation.pattern` is RE2 and anchored to the whole value — write
  `[a-z]+`, not `^[a-z]+$`. No `pattern` on `boolean`/`select`.
  `min`/`max` only on `number`; `min_length`/`max_length` only on
  text-like types.
- `flex` must be positive and requires `row`. Every group defined under
  `groups:` must be referenced by at least one variable.
- `when:` is `var` plus exactly one operator (`eq`, `ne`, `in`, `gt`,
  `gte`, `lt`, `lte`, `set`). `var` names another declared variable; no
  cycles; `eq`/`in` against a `select` must use its option values; a
  **configuration variable cannot depend on a deploy-time variable** (mark
  the dependent one `deploy_time: true` too).
- An output that shares a secret variable's name must declare
  `secret: true`. Outputs accept only `label`, `description`, `secret`.

## How steps run

- One script assembled from the steps runs in the container with `/bin/sh`
  under `set -e`; each step in its own subshell. The first failing command
  ends the step, marks it failed, and skips every later step. No retries,
  no rollback.
- `export`s carry forward to later steps and to output capture; `cd`,
  `set` options, functions, and unexported shell variables do not.
- The repository is mounted read-write at `/workspace`, which is the
  working directory. There is **no Docker daemon** inside the container —
  use a remote builder (Cloud Build, CodeBuild, ACR Tasks) or build in CI
  and pass an image tag as a deploy-time variable.
- Every **active** variable is exported by name. Inactive (`when` false)
  and optional-without-value variables are not exported at all — read them
  as `${NAME:-}`. Unset booleans resolve to `false`.
- Secret values are replaced with `•••` in logs (exact substrings only).
  Never `echo` credentials. Keep single log lines under 1 MB.
- Outputs are read from the environment after the last step succeeds
  (64 KiB max per value). Failed and canceled runs capture nothing.

## Choosing variable shapes

| Situation | Do this |
|---|---|
| Credential, token, key | `secret: true`, plus `manifest.why`/`how`; `type: multiline` for PEM/JSON |
| Fixed set of values (region, mode) | `type: select` with `{value, label}` options and a `default` |
| Tuning value with a sane default | `required: false`, `default: "…"`, `validation: {min, max}` |
| Value that changes every run (image tag, plan/apply) | `deploy_time: true` — asked in a modal on every Deploy |
| Field that only applies sometimes | `when: {var: ACTION, eq: destroy}` — disabled otherwise, not exported |
| Dangerous action | deploy-time `select` for the action + a conditional confirmation with `validation: {pattern: destroy}` |
| Many variables | `groups:` on the deployment, `group`/`row`/`flex` on variables |
| Something people ask for after a deploy (URL, IP, image) | `export NAME=…` in a step and declare it under `outputs:` |

Write `label`, `description`, `manifest.why`, and `manifest.how` for the
person who will fill in the form — usually not the author. Use real
instructions ("Google Cloud console → IAM & Admin → Service accounts →
Keys → Add key → JSON"), Markdown is supported. Add a `manifest` block to
every value a client must supply and leave it off internal switches; the
block's presence puts the variable in the default manifest selection.

## Checklist before you finish

- [ ] `keel validate` passes.
- [ ] Every tool the steps call is installed in the environment Dockerfile
      (see the `keel-environments` skill).
- [ ] The login/authentication step comes first and fails fast
      (`aws sts get-caller-identity`, `gcloud auth …`).
- [ ] Secrets are `secret: true`, have no `default`, and are never echoed.
- [ ] Every operator-supplied value has `label`, `manifest.why`,
      `manifest.how`, and a `validation` hint where the format matters.
- [ ] Optional/inactive variables are read as `${NAME:-}` in steps.
- [ ] `keel.yaml` and `deploy/` are committed; `.keel/dev.db` is not
      (Keel writes `.keel/.gitignore` for you).
