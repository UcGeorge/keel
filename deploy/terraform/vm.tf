# The single VM running caddy (TLS) + keel-cloud + postgres via compose.
#
# Postgres data, repository checkouts, and TLS certificates live on a
# separate persistent disk, so the VM is deliberately replaceable: whenever
# the cloud-init template changes, terraform_data.vm_config forces a clean
# re-create (the static IP and the data disk survive, ~2 minutes downtime).

data "google_compute_image" "ubuntu" {
  family  = "ubuntu-2404-lts-amd64"
  project = "ubuntu-os-cloud"
}

resource "google_compute_disk" "data" {
  name = "keel-cloud-data"
  type = "pd-balanced"
  zone = var.zone
  size = var.data_disk_gb
}

locals {
  # With no domain of your own, sslip.io resolves <ip-with-dashes>.sslip.io
  # to that IP via public DNS — Let's Encrypt issues for it like any other
  # hostname, and it works the moment the VM is up. Setting var.domain later
  # changes the user-data, which replaces the VM (data disk and IP survive)
  # and re-issues TLS for the new name automatically.
  domain = var.domain != "" ? var.domain : "${replace(google_compute_address.keel.address, ".", "-")}.sslip.io"

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tftpl", {
    project       = var.project
    region        = var.region
    domain        = local.domain
    acme_email    = var.acme_email
    backup_bucket = var.backup_bucket
  })
}

resource "terraform_data" "vm_config" {
  input = sha256(local.user_data)
}

resource "google_compute_instance" "keel" {
  name                      = "keel-cloud"
  machine_type              = var.machine_type
  zone                      = var.zone
  tags                      = ["keel-cloud"]
  allow_stopping_for_update = true

  boot_disk {
    initialize_params {
      image = data.google_compute_image.ubuntu.self_link
      size  = var.boot_disk_gb
      type  = "pd-balanced"
    }
  }

  attached_disk {
    source      = google_compute_disk.data.id
    device_name = "keel-data"
  }

  network_interface {
    subnetwork = google_compute_subnetwork.keel.id

    access_config {
      nat_ip = google_compute_address.keel.address
    }
  }

  metadata = {
    user-data      = local.user_data
    enable-oslogin = "TRUE"
  }

  service_account {
    email  = google_service_account.vm.email
    scopes = ["cloud-platform"]
  }

  lifecycle {
    replace_triggered_by = [terraform_data.vm_config]
  }

  depends_on = [
    google_project_service.apis,
    google_secret_manager_secret_version.db_password,
    google_secret_manager_secret_version.encryption_key,
    google_secret_manager_secret_iam_member.vm_db_password,
    google_secret_manager_secret_iam_member.vm_encryption_key,
  ]
}
