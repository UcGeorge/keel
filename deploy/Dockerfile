# deploy/Dockerfile — the environment Keel's own deployment runs in.
#
# The cloud-gcp deployment (see keel.yaml) needs: gcloud (auth, Cloud Build,
# IAP SSH, GCS), terraform, git, and an ssh client. Nothing here runs a
# Docker daemon — image builds happen remotely on Cloud Build.

FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:slim

ARG TERRAFORM_VERSION=1.9.8

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       curl unzip git openssh-client ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && arch="$(dpkg --print-architecture)" \
    && curl -fsSLo /tmp/terraform.zip \
       "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${arch}.zip" \
    && unzip -o /tmp/terraform.zip -d /usr/local/bin \
    && rm -f /tmp/terraform.zip \
    && terraform -version

ENV CLOUDSDK_CORE_DISABLE_PROMPTS=1

# Keel mounts the repository at /workspace and runs every step there.
WORKDIR /workspace
