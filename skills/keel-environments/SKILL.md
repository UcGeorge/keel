---
name: keel-environments
description: Write and debug the Dockerfile that defines a Keel deployment environment (the `environment:` block in keel.yaml) — vendor CLI base images for AWS, Google Cloud, Azure, Terraform, and Kubernetes, pinned tool versions, entrypoint and prompt rules, build context and caching. Use when creating deploy/*.Dockerfile for Keel, when a Keel step fails with a missing tool, or when a step wants to run docker inside the run.
license: MIT
metadata:
  author: keel
  source: https://github.com/UcGeorge/keel
  docs: https://keel-cloud.mintlify.site/guides/environment-images
---

# Keel environment images

A Keel deployment runs its steps inside a container built from
`environment.dockerfile` (build context `environment.context`, default
`.`). The image is the one thing every person running the deployment
would otherwise have to install: **everything the steps call must be in
it.** Keel mounts the repository at `/workspace`, runs each step with
`/bin/sh`, and exports every active variable into the environment.

Docs: https://keel-cloud.mintlify.site/guides/environment-images and
https://keel-cloud.mintlify.site/reference/run-environment.

## Rules of the container

- **`WORKDIR /workspace`** — the repository is mounted there and it is the
  working directory; relative paths in steps depend on it.
- **Steps run with `/bin/sh`.** On Alpine that is BusyBox `ash`; install
  `bash` and use `#!/usr/bin/env bash` in scripts that need it.
- **Reset the entrypoint** (`ENTRYPOINT []`) of images that ship the CLI as
  their entrypoint (`amazon/aws-cli` does). Keel starts the container with
  its own command.
- **Disable interactive prompts:** `CLOUDSDK_CORE_DISABLE_PROMPTS=1`,
  `AWS_PAGER=""`, `DEBIAN_FRONTEND=noninteractive`. Nobody is at the
  keyboard.
- **No Docker daemon inside.** A step cannot `docker build` or
  `docker push`. Use the cloud's remote builder (Cloud Build, CodeBuild,
  ACR Tasks, Kaniko in a cluster), or build in CI and pass the tag to the
  deployment as a deploy-time variable. Remote builders may run the
  *legacy* Docker builder: a Dockerfile that uses `FROM
  --platform=$BUILDPLATFORM`, `TARGETOS`/`TARGETARCH`, `RUN --mount`, or
  heredocs needs BuildKit — on Cloud Build put `env: [DOCKER_BUILDKIT=1]`
  on the `gcr.io/cloud-builders/docker` step, or the build fails with
  `failed to parse platform : "" is an invalid component`.
- **Disposable.** Nothing outside `/workspace` survives the run; nothing
  inside it is committed anywhere.
- **Pin versions.** The point of the environment is that the deployment
  behaves the same on every machine; `latest` defeats that.

## Starting points

```dockerfile
# deploy/aws.Dockerfile
FROM amazon/aws-cli:2.22.0
RUN yum install -y jq git && yum clean all
ENV AWS_PAGER=""
WORKDIR /workspace
ENTRYPOINT []
```

```dockerfile
# deploy/gcp.Dockerfile
FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:slim
RUN apt-get update && apt-get install -y --no-install-recommends jq git \
    && rm -rf /var/lib/apt/lists/*
ENV CLOUDSDK_CORE_DISABLE_PROMPTS=1
WORKDIR /workspace
```

```dockerfile
# deploy/azure.Dockerfile
FROM mcr.microsoft.com/azure-cli:2.67.0
RUN apk add --no-cache jq git
WORKDIR /workspace
```

```dockerfile
# deploy/terraform.Dockerfile — gcloud + terraform
FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:slim
ARG TERRAFORM_VERSION=1.9.8
RUN apt-get update && apt-get install -y --no-install-recommends curl unzip git \
    && rm -rf /var/lib/apt/lists/* \
    && arch="$(dpkg --print-architecture)" \
    && curl -fsSLo /tmp/terraform.zip \
       "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${arch}.zip" \
    && unzip -o /tmp/terraform.zip -d /usr/local/bin && rm /tmp/terraform.zip
ENV CLOUDSDK_CORE_DISABLE_PROMPTS=1
WORKDIR /workspace
```

```dockerfile
# deploy/k8s.Dockerfile — kubectl + helm
FROM alpine:3.20
ARG KUBECTL_VERSION=v1.31.2
ARG HELM_VERSION=v3.16.2
RUN apk add --no-cache curl bash git ca-certificates \
    && arch="$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')" \
    && curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl" \
    && chmod +x /usr/local/bin/kubectl \
    && curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${arch}.tar.gz" \
       | tar -xzO "linux-${arch}/helm" > /usr/local/bin/helm && chmod +x /usr/local/bin/helm
WORKDIR /workspace
```

The starter written by `keel init` is plain `alpine:3.20` with `bash`,
`curl`, `git` — replace it with a vendor base once the steps need real
tools.

## Build context and caching

```yaml
environment:
  dockerfile: deploy/aws.Dockerfile
  context: deploy          # default "."
```

The context is what `COPY` can see. Most environment images copy nothing,
so a narrow context (`deploy`) keeps builds fast and stops application
changes from invalidating layers. With the repository root as context,
add a `.dockerignore` excluding `.git`, `node_modules`, build output, and
`.keel`. Keel tags images per deployment (`keel/dev-<deployment>`,
`keel/cloud-<repo>-<deployment>`), so an unchanged Dockerfile rebuilds
from cache in seconds; order instructions from least to most frequently
changing.

Deployments that need the same tools can share one Dockerfile.
Deployments with different tools should get separate, smaller images.

## Debugging a missing tool without Keel

```console
$ docker build -f deploy/aws.Dockerfile -t try-aws .
$ docker run --rm -it -v "$PWD:/workspace" -w /workspace try-aws sh
```

This is close to what a run sees, minus the variables. Add a
`docker build -f deploy/aws.Dockerfile .` job in CI so a broken package
pin fails there, not in a client's deploy.
