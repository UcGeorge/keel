variable "project" {
  description = "GCP project ID that hosts this Keel Cloud."
  type        = string
}

variable "region" {
  description = "Region for the subnetwork, static IP, and Artifact Registry."
  type        = string
}

variable "zone" {
  description = "Zone for the VM and its disks (must be in var.region)."
  type        = string
}

variable "domain" {
  description = "Public hostname of this Keel Cloud (an A record must point at the static IP). Empty = derive a free <ip>.sslip.io hostname from the static IP — no domain ownership needed."
  type        = string
  default     = ""
}

variable "acme_email" {
  description = "Email Let's Encrypt uses for certificate expiry notices."
  type        = string
}

variable "machine_type" {
  description = "VM machine type."
  type        = string
  default     = "e2-medium"
}

variable "boot_disk_gb" {
  description = "Boot disk size (OS + Docker images live here)."
  type        = number
  default     = 40
}

variable "data_disk_gb" {
  description = "Persistent data disk size (Postgres data, repo checkouts, TLS certificates). Survives VM replacement."
  type        = number
  default     = 20
}

variable "db_password" {
  description = "PostgreSQL password for the keel database user."
  type        = string
  sensitive   = true
}

variable "encryption_key" {
  description = "64-hex-char AES-256 key Keel uses to encrypt variable values at rest."
  type        = string
  sensitive   = true
}

variable "backup_bucket" {
  description = "Existing GCS bucket nightly database dumps are written to. Created outside terraform so backups survive `terraform destroy`."
  type        = string
}
