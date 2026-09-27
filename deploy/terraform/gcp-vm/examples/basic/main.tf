# Minimal root module: one private itervox VM in a new network.
#   tofu init && tofu plan -var project_id=my-project -var repo_url=https://github.com/you/project.git
terraform {
  required_version = ">= 1.6.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.50"
    }
  }
}

variable "project_id" {
  type = string
}

variable "repo_url" {
  type = string
}

provider "google" {
  project = var.project_id
  region  = "us-central1"
}

module "itervox" {
  source = "../.."

  project_id      = var.project_id
  repo_url        = var.repo_url
  itervox_version = "latest"

  # Credentials come from Secret Manager at service start (fetch-secrets.sh).
  secret_ids  = ["itervox-linear", "itervox-anthropic"]
  secrets_map = "LINEAR_API_KEY=itervox-linear ANTHROPIC_API_KEY=itervox-anthropic"

  start_service        = true
  enable_cleanup_timer = true
}

output "dashboard_tunnel_command" {
  value = module.itervox.dashboard_tunnel_command
}
