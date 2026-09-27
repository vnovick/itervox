variable "project_id" {
  description = "GCP project to create everything in."
  type        = string
}

variable "name" {
  description = "Base name for the VM and its resources (lowercase, starts with a letter)."
  type        = string
  default     = "itervox"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,40}$", var.name))
    error_message = "name must be lowercase letters, digits and dashes, starting with a letter (max 41 chars)."
  }
}

variable "region" {
  description = "Region for the subnet, router and NAT."
  type        = string
  default     = "us-central1"
}

variable "zone" {
  description = "Zone for the VM and the data disk (a zonal disk can only attach in its own zone)."
  type        = string
  default     = "us-central1-a"
}

variable "machine_type" {
  description = "Compute Engine machine type (provision.sh default: e2-standard-4)."
  type        = string
  default     = "e2-standard-4"
}

variable "boot_disk_gb" {
  description = "Boot disk size in GB."
  type        = number
  default     = 50
}

variable "data_disk_gb" {
  description = "Persistent data disk size in GB. Holds the service HOME (~/.itervox, ~/.claude, ~/.codex) and the checkout (CORE-062)."
  type        = number
  default     = 100
}

variable "data_disk_type" {
  description = "Data disk type."
  type        = string
  default     = "pd-balanced"
}

variable "snapshot_retention_days" {
  description = "Daily data-disk snapshots are kept this many days. 0 disables the snapshot schedule."
  type        = number
  default     = 14

  validation {
    condition     = var.snapshot_retention_days >= 0
    error_message = "snapshot_retention_days must be >= 0."
  }
}

variable "deletion_protection" {
  description = "Compute Engine deletion protection on the VM. The data disk additionally carries lifecycle.prevent_destroy (see README)."
  type        = bool
  default     = true
}

variable "network" {
  description = "Existing VPC network self link/name to use. Empty creates a dedicated network and subnet."
  type        = string
  default     = ""
}

variable "subnetwork" {
  description = "Existing subnetwork self link (required when network is set)."
  type        = string
  default     = ""
}

variable "subnet_cidr" {
  description = "CIDR of the subnet created when network is empty."
  type        = string
  default     = "10.42.0.0/24"
}

variable "create_nat" {
  description = "Create a Cloud Router + Cloud NAT so the VM (no public IP) can reach the tracker, GitHub and the model APIs."
  type        = bool
  default     = true
}

variable "iap_ssh_source_ranges" {
  description = "Source ranges allowed to SSH (tcp/22). The default is Google's IAP TCP-forwarding range; the dashboard is reached by forwarding 8090 over that SSH session, never by opening 8090."
  type        = list(string)
  default     = ["35.235.240.0/20"]
}

variable "dashboard_source_ranges" {
  description = "Source ranges allowed to reach the dashboard port directly. EMPTY BY DEFAULT: the daemon binds 127.0.0.1 and is reached over an IAP SSH tunnel. Setting this also needs server.host in WORKFLOW.md and a token."
  type        = list(string)
  default     = []
}

variable "dashboard_port" {
  description = "Dashboard port (server.port in WORKFLOW.md). Only used when dashboard_source_ranges is non-empty."
  type        = number
  default     = 8090
}

variable "public_ip" {
  description = "Give the VM an ephemeral public IP. Off by default (Cloud NAT provides egress)."
  type        = bool
  default     = false
}

variable "image" {
  description = "Boot image (provision.sh default: Debian 12)."
  type        = string
  default     = "debian-cloud/debian-12"
}

variable "repo_url" {
  description = "Git URL of the project itervox operates on (passed to bootstrap.sh --repo). Not a secret: put no credentials in it."
  type        = string

  validation {
    condition     = !can(regex("://[^/@]+@", var.repo_url))
    error_message = "repo_url must not embed credentials (user:token@). Use a deploy key or GH_TOKEN from the secret manager."
  }
}

variable "itervox_version" {
  description = "itervox release tag to install, or \"latest\"."
  type        = string
  default     = "latest"
}

variable "install_root" {
  description = "Mount point of the data disk and parent of the checkout (bootstrap.sh --root)."
  type        = string
  default     = "/srv/itervox"
}

variable "start_service" {
  description = "Enable and start itervox at the end of first boot. Leave false until credentials exist (secret manager or .itervox/.env)."
  type        = bool
  default     = false
}

variable "enable_cleanup_timer" {
  description = "Enable itervox-cleanup.timer (prunes idle workspaces and old logs; deploy/cleanup.sh)."
  type        = bool
  default     = false
}

variable "secret_ids" {
  description = "Secret Manager secret IDs the VM may read (roles/secretmanager.secretAccessor on each secret only, never project-wide)."
  type        = list(string)
  default     = []
}

variable "secrets_map" {
  description = "ITERVOX_SECRETS_MAP for deploy/lib/fetch-secrets.sh: space-separated ENV_NAME=secret-id pairs. Names only, never values."
  type        = string
  default     = ""
}

variable "labels" {
  description = "Labels applied to every labelable resource."
  type        = map(string)
  default     = {}
}
