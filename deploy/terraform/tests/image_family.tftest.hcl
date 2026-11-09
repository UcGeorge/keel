# Mocked only: these runs never authenticate to GCP or create infrastructure.
mock_provider "google" {}

variables {
  project        = "keel-test-project"
  region         = "us-central1"
  zone           = "us-central1-a"
  domain         = "keel.example.test"
  acme_email     = "owner@example.test"
  db_password    = "test-password"
  encryption_key = "0000000000000000000000000000000000000000000000000000000000000000"
  backup_bucket  = "keel-test-backups"
}

run "create_vm" {
  command = apply

  override_data {
    target = data.google_compute_image.ubuntu
    values = {
      self_link = "https://www.googleapis.com/compute/v1/projects/ubuntu-os-cloud/global/images/ubuntu-2404-original"
    }
  }

  assert {
    condition     = google_compute_instance.keel.boot_disk[0].initialize_params[0].image == "https://www.googleapis.com/compute/v1/projects/ubuntu-os-cloud/global/images/ubuntu-2404-original"
    error_message = "A new VM must use the resolved Ubuntu image."
  }
}

run "family_advances" {
  command = plan

  override_data {
    target = data.google_compute_image.ubuntu
    values = {
      self_link = "https://www.googleapis.com/compute/v1/projects/ubuntu-os-cloud/global/images/ubuntu-2404-newer"
    }
  }

  assert {
    condition     = google_compute_instance.keel.boot_disk[0].initialize_params[0].image == "https://www.googleapis.com/compute/v1/projects/ubuntu-os-cloud/global/images/ubuntu-2404-original"
    error_message = "An advancing Ubuntu image family must not replace an existing VM."
  }
}
