# Deploying Keel Cloud (with Keel)

Keel deploys itself: the `cloud-gcp` deployment in this repository's own
[`keel.yaml`](../keel.yaml) stands up a complete, production-ready Keel
Cloud on Google Cloud. This document explains the architecture, what it
costs, and how to operate it.

**Intended scale: one company and a few of its clients** — a handful of
organizations, tens of users, a few deploys a day. The design optimizes for
cost and operational simplicity at that scale, not for horizontal scale.

## Why a single VM

Keel Cloud executes deployment runs on a **real Docker daemon** (it builds
each deployment's environment image and runs steps in containers with
bind-mounted workspaces). That rules out serverless containers — Cloud Run,
App Engine, ECS/Fargate and friends don't expose a daemon — and makes
Kubernetes (~$75+/month before the first node on GKE) pure overhead at this
scale. One VM running Docker is the honest fit: everything on it is
replaceable, and the parts that aren't live on a separate disk or in GCS.

```
                          ┌─ GCP project ─────────────────────────────────┐
   your team ── HTTPS ──▶ │  VM "keel-cloud" (e2-medium, static IP)       │
                          │  ┌─────────┐  ┌────────────┐  ┌─────────────┐ │
   Let's Encrypt ◀──────▶ │  │  caddy  │─▶│ keel-cloud │─▶│ postgres 16 │ │
                          │  └─────────┘  └─────┬──────┘  └──────┬──────┘ │
                          │        Docker  ◀────┘ runs           │        │
                          │  ───────────────────────────────────────────  │
                          │  data disk: pgdata · checkouts · TLS certs    │
                          │                                               │
                          │  Artifact Registry ◀── Cloud Build (images)   │
                          │  GCS: backups (nightly pg_dump) · tf state    │
                          │  Secret Manager: db password · encryption key │
                          └───────────────────────────────────────────────┘
```

Key choices:

- **caddy** terminates TLS with automatic Let's Encrypt certificates —
  no certificate management, ever.
- **A domain is optional.** Leave the *Domain* variable empty and Terraform
  derives `https://<ip-with-dashes>.sslip.io` from the static IP —
  [sslip.io](https://sslip.io) is public DNS that resolves the name back to
  that IP, so the URL works (with a real certificate) the moment the VM is
  up. Set the variable later and deploy again to switch to your own domain;
  the VM is replaced (~2 min), data and IP survive, and the new certificate
  issues itself. One caveat: Let's Encrypt rate limits are shared by all
  sslip.io users, so issuance can occasionally be slow — Caddy retries and
  also falls back to ZeroSSL on its own.
- **The data disk is separate from the VM.** Postgres data, repository
  checkouts, and TLS certificates live on `/mnt/keel-data`. The VM is
  *deliberately disposable*: when the server configuration (cloud-init)
  changes, Terraform replaces the whole VM (~2 minutes of downtime) while
  the disk and static IP survive. No configuration drift.
- **Images are built remotely on Cloud Build**, because Keel's own
  deployment steps run in a container without a Docker daemon. The free
  tier comfortably covers this usage.
- **SSH only via IAP.** Port 22 is never open to the internet; rollouts go
  through Google's identity-aware tunnel using the deployer service
  account.
- **Secrets never touch instance metadata.** The DB password and the
  at-rest encryption key sit in Secret Manager; the VM fetches them at
  first boot with its own identity.
- **Backups outlive everything.** Nightly `pg_dump` gzips stream to a GCS
  bucket that Terraform does not own — `destroy` cannot delete it, and the
  destroy path attempts one final backup before tearing down. The
  Terraform state bucket (versioned) is kept outside Terraform for the
  same reason.
- **Disk hygiene is automated:** a weekly `docker system prune` clears old
  client environment images; Artifact Registry keeps the 10 most recent
  keel-cloud images and drops the rest after 30 days; container logs are
  capped.

## What it costs (us-central1, monthly, approximate)

| Item | Cost |
|---|---|
| e2-medium VM (2 vCPU, 4 GB) | ~$24.50 |
| Boot disk 40 GB + data disk 20 GB (pd-balanced) | ~$6.00 |
| Static external IPv4 | ~$3.65 |
| GCS backups + Terraform state | < $0.50 |
| Artifact Registry, Secret Manager, egress | < $1.00 |
| Cloud Build | free tier |
| **Total** | **≈ $35** |

Very light use fits on `e2-small` (**≈ $22/month** total); heavy Terraform
runs by clients may want `e2-standard-2` (≈ $60). Change the machine type
in the target's variables and deploy — it applies with a short restart.

## First deploy

1. Create a **dedicated GCP project** with billing enabled.
2. Create a `keel-deployer` service account in it, grant **Owner** *on that
   project*, download a JSON key. (Least-privilege alternative below.)
3. Run `keel dev` in this repository, open the `cloud-gcp` deployment,
   create a target (e.g. `production`), and fill in the form — every field
   documents itself, and *Export variable manifest* produces a document you
   can hand to whoever supplies the values.
4. Press **Deploy** → *Deploy, full*. The first run takes ~10 minutes
   (API enablement + VM boot). It ends by printing the **Keel Cloud URL**
   and the **Server IP**.
5. **No domain?** Nothing to do — with *Domain* left empty the URL is a
   `…sslip.io` address that is already live (the run waits out certificate
   issuance for you). **Own domain?** Create an **A record** pointing at
   the Server IP; HTTPS goes live automatically a couple of minutes after
   DNS resolves — no second deploy needed. Either way, the `SITE_STATUS`
   output tells you where things stand.
6. Open the URL, create the first account, invite the team.

Subsequent deploys (new Keel version, config changes) are just **Deploy →
full** again: build → push → health-checked rollover, typically ~3 minutes.
Pick **Plan** in the deploy modal to preview Terraform changes without
touching anything.

## Operations

- **Restore a backup** (backups are `gs://<project>-keel-backups/pg/*.sql.gz`):

  ```console
  $ gcloud compute ssh keel-cloud --zone <zone> --tunnel-through-iap
  $ gcloud storage cp gs://<project>-keel-backups/pg/keel-<ts>.sql.gz - \
      | gunzip | sudo docker exec -i keel-db psql -U keel keel
  ```

- **Logs / status on the server:** `sudo docker logs keel-cloud`,
  `sudo docker ps`, `systemctl list-timers keel-*`.
- **Resize disks:** raise `BOOT_DISK_GB` / `DATA_DISK_GB` and deploy, then
  grow the filesystem (`sudo resize2fs /dev/disk/by-id/google-keel-data`
  for the data disk).
- **Rotate the DB password:** update the variable, deploy, then update the
  password inside Postgres to match
  (`ALTER USER keel WITH PASSWORD '…'`) and
  `sudo reboot` — the VM re-reads secrets at boot. Rotating
  `ENCRYPTION_KEY` is **not supported** — values encrypted with the old key
  become unreadable.
- **Destroy:** Deploy → *Destroy the infrastructure* → type `destroy`.
  Takes a final backup, removes the VM, disks, IP, network, registry, and
  secrets. The backup and state buckets remain; delete them manually if
  you truly want nothing left.
- **Multiple environments:** each Keel target keeps its own Terraform
  state (`keel-cloud/<target>` prefix), so a `staging` target with its own
  project/domain coexists cleanly with `production`.

## Least-privilege deployer (optional)

Owner-on-a-dedicated-project is the recommended simple setup. If your
organization requires granular roles, grant the deployer service account:
`roles/compute.admin`, `roles/iam.serviceAccountAdmin`,
`roles/iam.serviceAccountUser`, `roles/resourcemanager.projectIamAdmin`,
`roles/artifactregistry.admin`, `roles/secretmanager.admin`,
`roles/storage.admin`, `roles/cloudbuild.builds.editor`,
`roles/serviceusage.serviceUsageAdmin`, `roles/compute.osAdminLogin`, and
`roles/iap.tunnelResourceAccessor`.

## The GitHub App (optional)

Push-triggered auto-deploys need the GitHub App environment variables
(see [keel-cloud.md](keel-cloud.md)). Add them to
`/opt/keel/secrets.env` on the VM (`KEEL_GITHUB_APP_ID=…` etc.) and
`sudo docker compose --project-directory /opt/keel --env-file /opt/keel/image.env -f /opt/keel/compose.yaml up -d`
to apply — or extend `deploy/terraform/templates/cloud-init.yaml.tftpl`
with additional secrets following the existing pattern.
