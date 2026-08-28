# Everything except the VM itself: APIs, network, IP, firewall, the VM's
# service account, the image registry, secrets, and backup-bucket access.

locals {
  apis = [
    "compute.googleapis.com",
    "artifactregistry.googleapis.com",
    "cloudbuild.googleapis.com",
    "iap.googleapis.com",
    "secretmanager.googleapis.com",
    "logging.googleapis.com",
    "monitoring.googleapis.com",
  ]
}

resource "google_project_service" "apis" {
  for_each           = toset(local.apis)
  service            = each.value
  disable_on_destroy = false
}

# --- Network -----------------------------------------------------------------
# A dedicated VPC (free) instead of the default network, which hardened
# projects often don't have. The VM keeps a public IP, so no Cloud NAT.

resource "google_compute_network" "keel" {
  name                    = "keel"
  auto_create_subnetworks = false
  depends_on              = [google_project_service.apis]
}

resource "google_compute_subnetwork" "keel" {
  name          = "keel"
  network       = google_compute_network.keel.id
  region        = var.region
  ip_cidr_range = "10.20.0.0/24"
}

# Static IP so the DNS A record survives VM replacement.
resource "google_compute_address" "keel" {
  name   = "keel-cloud"
  region = var.region
}

resource "google_compute_firewall" "web" {
  name          = "keel-allow-web"
  network       = google_compute_network.keel.id
  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["keel-cloud"]

  allow {
    protocol = "tcp"
    ports    = ["80", "443"]
  }
}

# SSH only through IAP (the identity-aware TCP tunnel) — port 22 is never
# open to the internet. `gcloud compute ssh --tunnel-through-iap` uses this.
resource "google_compute_firewall" "iap_ssh" {
  name          = "keel-allow-iap-ssh"
  network       = google_compute_network.keel.id
  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["keel-cloud"]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

# --- VM identity -------------------------------------------------------------

resource "google_service_account" "vm" {
  account_id   = "keel-vm"
  display_name = "Keel Cloud VM"
  depends_on   = [google_project_service.apis]
}

resource "google_project_iam_member" "vm_ar_reader" {
  project = var.project
  role    = "roles/artifactregistry.reader"
  member  = "serviceAccount:${google_service_account.vm.email}"
}

resource "google_project_iam_member" "vm_log_writer" {
  project = var.project
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.vm.email}"
}

resource "google_project_iam_member" "vm_metric_writer" {
  project = var.project
  role    = "roles/monitoring.metricWriter"
  member  = "serviceAccount:${google_service_account.vm.email}"
}

# The backup bucket itself is created by the keel.yaml auth step (not by
# terraform) so `terraform destroy` can never delete the backups. Only this
# IAM grant is managed here.
resource "google_storage_bucket_iam_member" "vm_backup_writer" {
  bucket = var.backup_bucket
  role   = "roles/storage.objectCreator"
  member = "serviceAccount:${google_service_account.vm.email}"
}

# --- Image registry ----------------------------------------------------------

resource "google_artifact_registry_repository" "keel" {
  location      = var.region
  repository_id = "keel"
  format        = "DOCKER"
  depends_on    = [google_project_service.apis]

  cleanup_policies {
    id     = "keep-recent"
    action = "KEEP"
    most_recent_versions {
      keep_count = 10
    }
  }

  cleanup_policies {
    id     = "delete-stale"
    action = "DELETE"
    condition {
      older_than = "2592000s" # 30 days
    }
  }
}

# --- Secrets -----------------------------------------------------------------
# The VM fetches these at first boot (see templates/cloud-init.yaml.tftpl),
# so they never appear in instance metadata.

resource "google_secret_manager_secret" "db_password" {
  secret_id  = "keel-db-password"
  depends_on = [google_project_service.apis]

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "db_password" {
  secret      = google_secret_manager_secret.db_password.id
  secret_data = var.db_password
}

resource "google_secret_manager_secret" "encryption_key" {
  secret_id  = "keel-encryption-key"
  depends_on = [google_project_service.apis]

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "encryption_key" {
  secret      = google_secret_manager_secret.encryption_key.id
  secret_data = var.encryption_key
}

resource "google_secret_manager_secret_iam_member" "vm_db_password" {
  secret_id = google_secret_manager_secret.db_password.id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.vm.email}"
}

resource "google_secret_manager_secret_iam_member" "vm_encryption_key" {
  secret_id = google_secret_manager_secret.encryption_key.id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.vm.email}"
}
