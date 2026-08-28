# deploy/Dockerfile — the Keel deployment environment
#
# This image defines WHERE your deployment steps run. Install every tool the
# steps need: cloud CLIs (awscli, gcloud, az), terraform, kubectl, helm, ...
#
# Example for AWS:
#   FROM amazon/aws-cli:latest
# Example for GCP:
#   FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:slim
#
# The starter uses plain alpine so the sample deployment runs anywhere.
FROM alpine:3.20

RUN apk add --no-cache bash curl git

# Keel mounts your repository at /workspace and runs every step there.
WORKDIR /workspace
