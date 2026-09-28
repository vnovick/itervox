variable "name" {
  description = "Base name for the VM and its resources."
  type        = string
  default     = "itervox"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,40}$", var.name))
    error_message = "name must be lowercase letters, digits and dashes, starting with a letter (max 41 chars)."
  }
}

variable "location" {
  description = "Azure region (provision.sh default: eastus)."
  type        = string
  default     = "eastus"
}

variable "resource_group_name" {
  description = "Resource group to create (create_resource_group = true) or reuse."
  type        = string
  default     = "itervox-rg"
}

variable "create_resource_group" {
  description = "Create resource_group_name. False reuses an existing group."
  type        = bool
  default     = true
}

variable "vm_size" {
  description = "VM size (provision.sh default: Standard_D4s_v5)."
  type        = string
  default     = "Standard_D4s_v5"
}

variable "admin_username" {
  description = "Admin user for SSH (via Bastion or Tailscale; no public IP)."
  type        = string
  default     = "itervoxadmin"
}

variable "admin_ssh_public_key" {
  description = "OpenSSH PUBLIC key for admin_username. Password login is disabled."
  type        = string
}

variable "os_disk_gb" {
  description = "OS disk size in GB."
  type        = number
  default     = 64
}

variable "data_disk_gb" {
  description = "Persistent managed data disk size in GB. Holds the service HOME (~/.itervox, ~/.claude, ~/.codex) and the checkout (CORE-062)."
  type        = number
  default     = 100
}

variable "data_disk_sku" {
  description = "Managed data disk SKU."
  type        = string
  default     = "Premium_LRS"
}

variable "data_disk_lock" {
  description = "CanNotDelete management lock on the data disk: Azure itself refuses to delete it (portal, CLI or tofu) until the lock is removed. The disk also carries lifecycle.prevent_destroy."
  type        = bool
  default     = true
}

variable "backup_retention_days" {
  description = "Daily Azure Backup (Data Protection) snapshots of the data disk are kept this many days. 0 disables the backup vault."
  type        = number
  default     = 14

  validation {
    condition     = var.backup_retention_days == 0 || (var.backup_retention_days >= 1 && var.backup_retention_days <= 360)
    error_message = "backup_retention_days must be 0 or 1-360."
  }
}

variable "vnet_cidr" {
  description = "Address space of the VNet."
  type        = string
  default     = "10.43.0.0/16"
}

variable "subnet_cidr" {
  description = "VM subnet."
  type        = string
  default     = "10.43.1.0/24"
}

variable "create_nat" {
  description = "Attach a NAT gateway to the VM subnet (the VM has no public IP; bootstrap needs egress)."
  type        = bool
  default     = true
}

variable "dashboard_source_prefixes" {
  description = "Source prefixes allowed to reach the dashboard port. EMPTY BY DEFAULT: reach it over an SSH tunnel (Bastion/Tailscale), never an open port."
  type        = list(string)
  default     = []
}

variable "dashboard_port" {
  description = "Dashboard port (server.port in WORKFLOW.md). Only used when dashboard_source_prefixes is non-empty."
  type        = number
  default     = 8090
}

variable "repo_url" {
  description = "Git URL of the project itervox operates on (bootstrap.sh --repo). Not a secret: put no credentials in it."
  type        = string

  validation {
    condition     = !can(regex("://[^/@]+@", var.repo_url))
    error_message = "repo_url must not embed credentials (user:token@). Use a deploy key or GH_TOKEN from Key Vault."
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
  description = "Enable and start itervox at the end of first boot. Leave false until credentials exist (Key Vault or .itervox/.env)."
  type        = bool
  default     = false
}

variable "enable_cleanup_timer" {
  description = "Enable itervox-cleanup.timer (prunes idle workspaces and old logs; deploy/cleanup.sh)."
  type        = bool
  default     = false
}

variable "key_vault_id" {
  description = "Key Vault resource id the VM identity may read secrets from (Key Vault Secrets User on that vault only). Empty grants nothing."
  type        = string
  default     = ""
}

variable "key_vault_name" {
  description = "Key Vault name passed to fetch-secrets.sh (ITERVOX_SECRETS_AZURE_VAULT)."
  type        = string
  default     = ""
}

variable "secrets_map" {
  description = "ITERVOX_SECRETS_MAP for deploy/lib/fetch-secrets.sh: space-separated ENV_NAME=secret-name pairs. Names only, never values."
  type        = string
  default     = ""
}

variable "tags" {
  description = "Tags applied to every taggable resource."
  type        = map(string)
  default     = {}
}
