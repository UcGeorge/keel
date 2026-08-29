# `keel.yaml` — complete schema

Source of truth: https://keel-cloud.mintlify.site/reference/keel-yaml (and
the sibling pages for variable types, validation, conditions, layout,
outputs, manifest, and the run environment). `keel.yaml` or `keel.yml` at
the repository root; Keel Dev re-reads it on every page load, Keel Cloud on
every sync and push. Unknown keys anywhere are validation errors.

## Top level

| Key | Type | Notes |
|---|---|---|
| `version` | integer | required, must be `1` |
| `deployments` | mapping | required, at least one; name → deployment |

Deployment names: `[a-z0-9]([a-z0-9-]*[a-z0-9])?`, ≤ 63 characters, unique.

## Deployment

| Key | Type | Default | Notes |
|---|---|---|---|
| `description` | string | — | one-line plain text; cards, lists, manifest header |
| `long_description` | string (Markdown) | — | rendered on the deployment page |
| `groups` | mapping | — | group ID (integer) → label string or group definition |
| `environment` | mapping | — | **required** |
| `steps` | list | — | **required**, at least one |
| `variables` | mapping | — | name → variable; a bare `NAME:` is a required text variable |
| `outputs` | mapping | — | name → output; a bare `NAME:` uses all defaults |

### `environment`

| Key | Type | Default | Notes |
|---|---|---|---|
| `dockerfile` | string | — | **required**; relative path inside the repository |
| `context` | string | `.` | Docker build context; relative, inside the repository |

The image is tagged `keel/dev-<deployment>` (Keel Dev) or
`keel/cloud-<repo>-<deployment>` (Keel Cloud), so Docker layer cache
carries across runs.

### `steps[]`

| Key | Type | Notes |
|---|---|---|
| `name` | string | **required**; short label next to the status |
| `run` | string | **required**; shell executed with `/bin/sh`; multi-line allowed |

### `groups`

```yaml
groups:
  1: Credentials              # shorthand: a label
  2:
    label: Tuning
    description: Rarely needed.   # Markdown
    collapsed: true               # start closed; opens if it holds an error
```

Groups render in ascending ID order after ungrouped variables. A group
referenced but not defined renders as `Group <id>`; a defined group that
no variable references is an error.

## Variable

Names: `[A-Z][A-Z0-9_]*`, unique, not starting with `KEEL_`.

| Key | Type | Default | Notes |
|---|---|---|---|
| `label` | string | the name | form label and manifest heading |
| `type` | string | `text` | `text`, `multiline`, `number`, `email`, `url`, `boolean`, `select` |
| `secret` | boolean | `false` | encrypted at rest, password field, masked in logs; no `default`; not for `boolean`/`select` |
| `required` | boolean | `true` | inactive variables are never required |
| `description` | string (Markdown) | — | under the field and in the manifest |
| `placeholder` | string | — | form placeholder (non-secret fields fall back to the default) |
| `default` | string | — | pre-filled and used when blank; must pass validation; write numbers/booleans as strings |
| `options` | list | — | required for `select`, forbidden otherwise; `string` or `{value, label}`; values non-empty, unique |
| `validation` | mapping | — | `pattern`, `message`, `min`, `max`, `min_length`, `max_length` |
| `manifest` | mapping | — | `include`, `title`, `why`, `how`; presence includes the variable in the default manifest selection |
| `group` | integer | — | layout: which group |
| `row` | integer | — | layout: variables sharing a row ID within a group render side by side |
| `flex` | number | `1` | layout: share of the row's width; positive; requires `row` |
| `deploy_time` | boolean | `false` | asked in a modal every time a deploy starts; not stored on the target |
| `when` | mapping | — | `var` + exactly one of `eq`, `ne`, `in`, `gt`, `gte`, `lt`, `lte`, `set` |

### Types

| Type | Control | Built-in check | Extra validation keys |
|---|---|---|---|
| `text` | single-line input, trimmed | — | `pattern`, `min_length`, `max_length` |
| `multiline` | textarea, monospace, whitespace preserved | — | `pattern`, `min_length`, `max_length` |
| `number` | number input (`step=any`) | parses as a number | `min`, `max`, `pattern`, `min_length`, `max_length` |
| `email` | email input | `local@domain.tld` | `pattern`, `min_length`, `max_length` |
| `url` | url input | scheme and host present | `pattern`, `min_length`, `max_length` |
| `boolean` | true/false dropdown | `true` or `false`; unset → `false` | — |
| `select` | dropdown | one of `options` | — |

Every value is stored and exported as a string.

### `validation`

| Key | Applies to | Notes |
|---|---|---|
| `pattern` | all except `boolean`, `select` | RE2, anchored to the whole value |
| `message` | with `pattern` | error text and the format hint under the field |
| `min`, `max` | `number` | inclusive; `max ≥ min` |
| `min_length`, `max_length` | `text`, `multiline`, `email`, `url` | bytes; `min_length ≥ 0`, `max_length ≥ min_length` |

An empty value always passes value validation; whether a value is *needed*
is the separate required/default/active check.

### `manifest`

| Key | Default | Notes |
|---|---|---|
| `include` | `true` when the block exists | `include: false` excludes a variable that has a block |
| `title` | the label | heading of the entry |
| `why` | — | Markdown: why the value is needed |
| `how` | — | Markdown: how to obtain it |

### `when`

| Operator | Holds when the referenced effective value… |
|---|---|
| `eq: v` | equals `v` |
| `ne: v` | does not equal `v` |
| `in: [a, b]` | is one of the values (at least one) |
| `gt`, `gte`, `lt`, `lte: n` | compares numerically (both sides must parse) |
| `set: true` / `set: false` | has any value / is empty |

The effective value is the saved (or submitted, or deploy-time) value,
else the default. Conditions chain; an inactive variable makes every
condition reading it false. Rules: `var` names a declared variable other
than itself; no cycles; a configuration variable cannot depend on a
deploy-time variable; `eq`/`in` against a `select` must use its options.

Inactive means: field disabled with a hint, never required, not exported
into the run (read as `${NAME:-}`), listed as *Only applies when …* in the
manifest. Typed values are kept, not deleted.

## Output

Names follow variable rules and are unique.

| Key | Type | Default | Notes |
|---|---|---|---|
| `label` | string | the name | run page and target page |
| `description` | string (Markdown) | — | under the value |
| `secret` | boolean | `false` | encrypted, masked behind a reveal control; **required** when the output shares a secret variable's name |

Captured from the container environment after the last step succeeds —
`export`ed in any step or an input passed through. A non-secret output
whose value contains a secret input's value is treated as secret. Limits:
64 KiB per value. Failed/canceled runs capture nothing.

## Run environment

| | |
|---|---|
| Shell | `/bin/sh`, one assembled script under `set -e`, each step in its own subshell |
| Working directory | `/workspace` (the repository, mounted read-write) |
| Source | Keel Dev: your working tree; Keel Cloud: a fresh shallow clone of the connected branch; `keel deploy`: the current directory |
| Injected variables | `KEEL_DEPLOYMENT`, `KEEL_TARGET` (`cli` for `keel deploy` without `--target`), `KEEL_TARGET_ID` (stable across renames — key persistent state by this), `KEEL_RUN_ID` |
| Docker inside | none |
| Lifetime | container removed when the run ends or is canceled |
| Logs | streamed line by line; secret values replaced with `•••`; single lines over 1 MB drop that stream for the rest of the run |

Exposure rules, in order: inactive variables are not exported; a saved
value wins over the default; an unset boolean becomes `false`; an
optional variable with neither is not exported; a required variable with
neither blocks the deploy.
