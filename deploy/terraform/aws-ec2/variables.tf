variable "name" {
  description = "Base name for the instance and its resources."
  type        = string
  default     = "itervox"

  validation {
    condition     = can(regex("^[a-zA-Z][a-zA-Z0-9-]{0,40}$", var.name))
    error_message = "name must be letters, digits and dashes, starting with a letter (max 41 chars)."
  }
}

variable "vpc_id" {
  description = "VPC for the security group."
  type        = string
}

variable "subnet_id" {
  description = "Subnet for the instance. Needs egress (a NAT gateway for a private subnet, or associate_public_ip = true) to reach the tracker, GitHub and the model APIs. The data volume is created in this subnet's AZ."
  type        = string
}

variable "instance_type" {
  description = "EC2 instance type (provision.sh default: t3.xlarge). Graviton families (t4g, m7g, ...) select the arm64 AMI. Must be a Nitro type: the data volume is addressed as an NVMe device."
  type        = string
  default     = "t3.xlarge"
}

variable "ami_id" {
  description = "AMI to boot. Empty uses the latest Amazon Linux 2023 for the instance type's architecture (SSM public parameter), like provision.sh."
  type        = string
  default     = ""
}

variable "root_volume_gb" {
  description = "Root volume size in GB."
  type        = number
  default     = 50
}

variable "data_volume_gb" {
  description = "Persistent data volume size in GB. Holds the service HOME (~/.itervox, ~/.claude, ~/.codex) and the checkout (CORE-062)."
  type        = number
  default     = 100
}

variable "data_volume_type" {
  description = "EBS volume type of the data volume."
  type        = string
  default     = "gp3"
}

variable "kms_key_id" {
  description = "KMS key ARN for EBS encryption. Empty uses the account's default EBS key."
  type        = string
  default     = ""
}

variable "snapshot_retention_days" {
  description = "Daily Data Lifecycle Manager snapshots of the data volume are kept this many days (count-based retention). 0 disables the policy."
  type        = number
  default     = 14

  validation {
    condition     = var.snapshot_retention_days >= 0 && var.snapshot_retention_days <= 1000
    error_message = "snapshot_retention_days must be between 0 and 1000."
  }
}

variable "deletion_protection" {
  description = "EC2 termination protection (disable_api_termination). The data volume additionally carries lifecycle.prevent_destroy and final_snapshot (see README)."
  type        = bool
  default     = true
}

variable "associate_public_ip" {
  description = "Give the instance a public IP (for egress in a subnet without NAT). No inbound rule is opened either way; access is SSM Session Manager."
  type        = bool
  default     = false
}

variable "dashboard_cidr_blocks" {
  description = "CIDRs allowed to reach the dashboard port directly. EMPTY BY DEFAULT: reach it with SSM port forwarding (see the dashboard_tunnel_command output)."
  type        = list(string)
  default     = []
}

variable "dashboard_port" {
  description = "Dashboard port (server.port in WORKFLOW.md). Only used when dashboard_cidr_blocks is non-empty."
  type        = number
  default     = 8090
}

variable "repo_url" {
  description = "Git URL of the project itervox operates on (bootstrap.sh --repo). Not a secret: put no credentials in it."
  type        = string

  validation {
    condition     = !can(regex("://[^/@]+@", var.repo_url))
    error_message = "repo_url must not embed credentials (user:token@). Use a deploy key or GH_TOKEN from Secrets Manager."
  }
}

variable "itervox_version" {
  description = "itervox release tag to install, or \"latest\"."
  type        = string
  default     = "latest"
}

variable "install_root" {
  description = "Mount point of the data volume and parent of the checkout (bootstrap.sh --root)."
  type        = string
  default     = "/srv/itervox"
}

variable "start_service" {
  description = "Enable and start itervox at the end of first boot. Leave false until credentials exist (Secrets Manager or .itervox/.env)."
  type        = bool
  default     = false
}

variable "enable_cleanup_timer" {
  description = "Enable itervox-cleanup.timer (prunes idle workspaces and old logs; deploy/cleanup.sh)."
  type        = bool
  default     = false
}

variable "secret_arns" {
  description = "Secrets Manager secret ARNs the instance may read (secretsmanager:GetSecretValue on exactly these)."
  type        = list(string)
  default     = []
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
