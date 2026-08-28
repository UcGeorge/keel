# Keel Cloud on a single GCE VM — Terraform entry point.
#
# The state backend is GCS; the bucket is created (idempotently) by the
# keel.yaml "Authenticate" step and passed via -backend-config, so different
# Keel targets (staging, production) keep separate state under their own
# prefix.

terraform {
  required_version = ">= 1.7.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }

  backend "gcs" {}
}

provider "google" {
  project = var.project
  region  = var.region
  zone    = var.zone
}
