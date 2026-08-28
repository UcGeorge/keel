# Keel documentation

The Keel docs site, built with [Mintlify](https://mintlify.com). Pages are
MDX files; `docs.json` defines navigation, theme, and branding.

## Preview locally

```console
$ npm i -g mint
$ cd docs/mintlify
$ mint dev            # http://localhost:3000
```

## Check before pushing

```console
$ mint broken-links
$ mint validate
```

The `Docs` GitHub workflow runs the same two commands on every change under
`docs/mintlify/`. Mintlify deploys the site from the connected branch.

## Layout

| Path | Contents |
|---|---|
| `index.mdx`, `install.mdx`, `quickstart.mdx` | Get started |
| `concepts/` | How Keel works, one concept per page |
| `guides/` | Task-oriented walkthroughs |
| `cloud/` | Keel Cloud: hosting, organizations, repositories, GitHub App |
| `reference/` | Exhaustive `keel.yaml`, CLI, runtime, and configuration reference |

When you change behavior in the Go code, update the matching reference page
in the same change — the docs are meant to be complete without the source.
