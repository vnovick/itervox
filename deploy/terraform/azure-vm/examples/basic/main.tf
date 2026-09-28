# Minimal root module: one private itervox VM in a new resource group.
#   tofu init && tofu plan -var repo_url=https://github.com/you/project.git -var "admin_ssh_public_key=$(cat ~/.ssh/id_ed25519.pub)"
terraform {
  required_version = ">= 1.6.0"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.81"
    }
  }
}

variable "repo_url" {
  type = string
}

variable "admin_ssh_public_key" {
  type = string
}

provider "azurerm" {
  features {}
}

module "itervox" {
  source = "../.."

  repo_url             = var.repo_url
  admin_ssh_public_key = var.admin_ssh_public_key

  start_service        = false
  enable_cleanup_timer = true
}

output "vm_id" {
  value = module.itervox.vm_id
}
