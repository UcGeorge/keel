# Keel Cloud — operations guide

Keel Cloud is the multi-user, multi-repository Keel: the same engine and UI
as `keel dev`, plus organizations, members with roles and scopes, connected
repositories, and push-triggered deployments.

## Running it

> **Deploying to production?** Keel deploys itself: the repository's own
> `keel.yaml` provisions a complete single-VM Keel Cloud on Google Cloud —
> see [deploy-keel-cloud.md](deploy-keel-cloud.md) for the architecture,
> costs, and runbook. The rest of this section covers running the binary
> by hand.

Requirements on the host: **PostgreSQL 13+**, **Docker** (runs execute on
the host daemon), **git**.

The fastest path is the bundled compose file:

```console
$ cp .env.example .env       # set KEEL_ENCRYPTION_KEY and KEEL_BASE_URL
$ docker compose up --build
```

Or run the binary directly:

```console
$ make build-cloud
$ KEEL_DATABASE_URL=postgres://keel:keel@localhost:5432/keel?sslmode=disable ./bin/keel-cloud
```

Migrations are embedded and applied automatically at startup. `/healthz`
reports readiness.

### Configuration

| Variable | Default | Description |
|---|---|---|
| `KEEL_DATABASE_URL` | — (required) | PostgreSQL URL. |
| `KEEL_ADDR` | `:8080` | Listen address. |
| `KEEL_BASE_URL` | `http://localhost:8080` | External URL; used in invite links. `https://` turns on Secure cookies — always set it in production, behind TLS. |
| `KEEL_DATA_DIR` | `./keel-data` | Clone/run workspaces (and the generated encryption key if none is set). |
| `KEEL_ENCRYPTION_KEY` | generated | 64-char hex AES-256 key for variable values and repo tokens at rest. Generate with `openssl rand -hex 32` and back it up — losing it means re-entering every saved value. |

Run Keel Cloud behind a TLS-terminating reverse proxy (Caddy, nginx,
Traefik). SSE is used for live logs — disable response buffering for
`/orgs/*/repos/*/runs/*/events` if your proxy buffers by default.

## Organizations, roles, and scopes

Signing up creates a **personal organization**; anyone can create more and
invite people. Within an organization:

| Capability | owner | admin | member |
|---|---|---|---|
| View everything | ✓ | ✓ | ✓ |
| Deploy / cancel runs | ✓ | ✓ | with **deploy** scope |
| Create targets, edit variables | ✓ | ✓ | with **configure** scope |
| Connect repositories, repo settings | ✓ | ✓ | — |
| Invite members, edit member scopes | ✓ | ✓ (members only) | — |
| Invite/promote admins, change roles | ✓ | — | — |
| Rename / delete the organization | ✓ | — | — |

The organization always keeps at least one owner. Members without scopes
are read-only viewers. Invites are single-use links valid for 14 days,
created on the Members page and shared out-of-band.

## Connecting repositories

**Any git host (HTTPS).** Paste the clone URL and, for private
repositories, a read-only access token (stored encrypted, used only to
clone). Works with GitHub, GitLab, Bitbucket, Gitea, …

**GitHub App** (when configured, see below). Admins install the app on
their GitHub account/org; installed repositories appear in a picker, clone
authentication is automatic, and pushes to the connected branch:

1. re-sync `keel.yaml`, and
2. deploy every target of that repository marked **auto-deploy on push**
   (only when the target's variables are complete and no run is already
   active for it).

On connect (and on every sync) Keel clones the chosen branch, finds
`keel.yaml`, and validates it. An invalid or missing configuration still
connects — the repository page shows exactly what's wrong.

## Setting up the GitHub App

1. GitHub → *Settings → Developer settings → GitHub Apps → New GitHub App*.
2. Webhook URL: `<KEEL_BASE_URL>/webhooks/github`, with a strong webhook
   secret.
3. Repository permissions: **Contents: Read-only**, **Metadata: Read-only**.
4. Subscribe to events: **Push**, **Installation target**, **Installation
   repositories**.
5. Generate a private key; note the App ID and slug.
6. Configure Keel Cloud:

```bash
KEEL_GITHUB_APP_ID=123456
KEEL_GITHUB_APP_SLUG=your-keel-app
KEEL_GITHUB_PRIVATE_KEY_FILE=/etc/keel/github-app.pem
KEEL_GITHUB_WEBHOOK_SECRET=…
```

Install the app on the GitHub account that owns your repositories; the
installation registers itself via webhook and its repositories appear on
the *Connect a repository* page.

## How runs execute

Each run gets a fresh shallow clone of the connected branch in
`KEEL_DATA_DIR/runs/<run-id>` (deleted afterwards). The environment image
is built from the deployment's Dockerfile (tagged per repo+deployment, so
Docker layer caching keeps repeat runs fast), then the steps execute in a
container with the checkout mounted at `/workspace` and the target's
variables exported. Logs stream live over SSE and are persisted; secret
values are masked. One run per target at a time; runs interrupted by a
server restart are marked failed on boot.

## Security notes

- Passwords: argon2id. Sessions: opaque 256-bit tokens (hash stored),
  30-day expiry, HttpOnly/SameSite=Lax cookies, Secure under https.
- CSRF: per-session tokens on every mutating request (double-submit cookie
  before sign-in). Webhooks authenticate with HMAC-SHA256 signatures.
- Variable values and repository tokens are AES-256-GCM encrypted at rest.
- Deployment steps are arbitrary code from the connected repository,
  executed in Docker on this host — connect repositories you trust, and
  treat the Keel Cloud host as part of your deployment trust boundary.
