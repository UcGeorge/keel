output "instance_ip" {
  description = "Static public IP (with a custom domain, point its A record here)."
  value       = google_compute_address.keel.address
}

output "domain" {
  description = "Effective hostname — the custom domain, or the derived sslip.io name."
  value       = local.domain
}

output "app_url" {
  description = "Public URL of this Keel Cloud."
  value       = "https://${local.domain}"
}

output "image_repo" {
  description = "Artifact Registry Docker repository for keel-cloud images."
  value       = "${var.region}-docker.pkg.dev/${var.project}/${google_artifact_registry_repository.keel.repository_id}"
}

output "instance" {
  description = "VM instance name (for `gcloud compute ssh`)."
  value       = google_compute_instance.keel.name
}
