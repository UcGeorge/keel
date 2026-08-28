# `keel.yaml` patterns

Copy-and-adapt fragments for situations that come up in most deployments.
All of them validate with `keel validate`.

## Authentication first, fail fast

```yaml
steps:
  - name: Authenticate
    run: |
      printf '%s' "$GCP_SA_KEY_JSON" > /tmp/sa.json
      gcloud auth activate-service-account --key-file=/tmp/sa.json
      gcloud config set project "$GCP_PROJECT"
  - name: Deploy
    run: ./deploy/gcp/deploy.sh
```

For AWS end the login step with `aws sts get-caller-identity`; for Azure
`az account show`. A wrong credential then fails in seconds with a clear
message, before anything is built.

## Secret credential with operator instructions

```yaml
variables:
  GCP_SA_KEY_JSON:
    label: Service account key (JSON)
    type: multiline                 # preserves newlines; masked while typing
    secret: true
    manifest:
      why: Authenticates Terraform and gcloud against your project.
      how: |
        **IAM & Admin → Service accounts** → create `keel-deploy` with the
        *Editor* role → **Keys → Add key → JSON**. Paste the whole file.
```

Secrets: no `default`, not `boolean`/`select`, masked in logs (exact
substring only — do not print transformed forms).

## Fixed choices as a select

```yaml
  AWS_REGION:
    label: Region
    type: select
    default: us-east-1
    options:
      - {value: us-east-1, label: US East (N. Virginia)}
      - {value: eu-west-1, label: Europe (Ireland)}
```

## Plan/apply and destroy with confirmation (deploy-time + conditions)

```yaml
variables:
  ACTION:
    label: Action
    type: select
    deploy_time: true
    default: deploy
    options:
      - {value: deploy, label: Deploy}
      - {value: destroy, label: Destroy the infrastructure}
  RUN_MODE:
    label: Mode
    type: select
    deploy_time: true
    default: full
    options:
      - {value: full, label: "Full — provision and roll out"}
      - {value: plan, label: "Plan — preview only"}
    when: {var: ACTION, eq: deploy}
  CONFIRM_DESTROY:
    label: Type "destroy" to confirm
    deploy_time: true
    when: {var: ACTION, eq: destroy}
    validation:
      pattern: destroy
      message: Type "destroy" to confirm.

steps:
  - name: Terraform
    run: |
      case "${ACTION:-deploy}" in
        destroy)
          terraform -chdir=infra destroy -auto-approve ;;
        deploy)
          if [ "${RUN_MODE:-full}" = "plan" ]; then
            terraform -chdir=infra plan
          else
            terraform -chdir=infra apply -auto-approve
          fi ;;
      esac
```

Why every variable here is `deploy_time: true`: a configuration
(stored-on-target) variable cannot depend on a deploy-time one. Read
conditional variables as `${NAME:-}` — inactive ones are not exported.

## Image tag chosen per run

```yaml
  IMAGE_TAG:
    label: Image tag
    deploy_time: true
    placeholder: "e.g. 2024.11.3 or a commit SHA"
    validation: {pattern: "[A-Za-z0-9._-]+"}
```

Push-triggered auto-deploys in Keel Cloud use defaults, so a required
deploy-time variable without a default blocks auto-deploy.

## Large forms: groups, rows, flex

```yaml
groups:
  1: Google Cloud
  2:
    label: Sizing & retention
    description: Rarely needed — the defaults fit most teams.
    collapsed: true

variables:
  GCP_PROJECT:     {label: Project ID, group: 1, row: 1}
  GCP_REGION:      {label: Region, group: 1, row: 1, type: select, default: us-central1, options: [us-central1, europe-west1]}
  GCP_SA_KEY_JSON: {label: Service account key (JSON), group: 1, type: multiline, secret: true}
  MACHINE_TYPE:    {label: Machine type, group: 2, type: select, default: e2-medium, options: [e2-small, e2-medium, e2-standard-2]}
  DATA_DISK_GB:    {label: Data disk (GB), group: 2, type: number, default: "20", validation: {min: 10, max: 500}}
  IMAGE_TAG:       {label: Image tag, deploy_time: true}       # ungrouped: rendered first
```

Rows render side by side in document order; rowless variables come after
the rows of their group, full width — put `multiline` fields there.
`flex: 2` makes a field twice as wide as its row-mates.

## Outputs

```yaml
steps:
  - name: Terraform
    run: |
      terraform -chdir=deploy/terraform apply -auto-approve
      export VM_IP="$(terraform -chdir=deploy/terraform output -raw instance_ip)"
      export APP_URL="https://$(terraform -chdir=deploy/terraform output -raw domain)"
  - name: Verify
    run: curl -fsS --max-time 10 "$APP_URL/healthz"

outputs:
  APP_URL:
    label: Application URL
    description: Where your team signs in.
  VM_IP:
    label: Server IP
    description: Create an **A record** for your domain pointing here.
  DATABASE_URL:
    secret: true
    label: Database URL
    description: For emergency `psql` access on the VM.
```

Exports carry across steps; the final value is captured after the last
step succeeds.

## Optional tuning values

```yaml
  DESIRED_COUNT:
    label: Service replicas
    type: number
    required: false
    default: "2"
    validation: {min: 1, max: 20}
  NOTIFY_EMAIL:
    label: Notification email
    type: email
    required: false
    description: Deployment reports are sent here after every run.
```

## Several deployments, several clouds

```yaml
deployments:
  aws-production:
    environment: {dockerfile: deploy/aws.Dockerfile}
    …
  gcp-production:
    environment: {dockerfile: deploy/gcp.Dockerfile}
    …
  database-migrate:
    environment: {dockerfile: deploy/aws.Dockerfile}   # images can be shared
    …
```

Prefer one deployment per operation (`database-migrate` next to
`aws-production`) over a `RUN_MIGRATIONS` boolean, unless the operations
genuinely share a procedure — then a deploy-time `select` is the right tool.

## Starter produced by `keel init`

```yaml
version: 1
deployments:
  production:
    description: Deploy this project to production.
    environment:
      dockerfile: deploy/Dockerfile
      context: .
    steps:
      - name: Show environment
        run: |
          echo "Deploying $KEEL_DEPLOYMENT to target $KEEL_TARGET"
          echo "Greeting: $GREETING"
      - name: Deploy
        run: echo "Put your real deployment commands here."
    variables:
      GREETING:
        label: Greeting
        default: hello
        description: An example variable. Replace it with your real inputs.
        manifest:
          why: Demonstrates how a variable appears in a generated manifest.
          how: Any short text works.
```

Replace `production`, the steps, and `GREETING` — do not leave the sample
variable in a real configuration.
