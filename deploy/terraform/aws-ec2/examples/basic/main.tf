# Minimal root module: one itervox instance in an existing private subnet
# (with a NAT gateway for egress).
#   tofu init && tofu plan -var vpc_id=vpc-... -var subnet_id=subnet-... -var repo_url=https://github.com/you/project.git
terraform {
  required_version = ">= 1.6.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.66"
    }
  }
}

variable "vpc_id" {
  type = string
}

variable "subnet_id" {
  type = string
}

variable "repo_url" {
  type = string
}

provider "aws" {
  region = "us-east-1"
}

module "itervox" {
  source = "../.."

  vpc_id    = var.vpc_id
  subnet_id = var.subnet_id
  repo_url  = var.repo_url

  # Credentials come from Secrets Manager at service start (fetch-secrets.sh).
  secret_arns = ["arn:aws:secretsmanager:us-east-1:123456789012:secret:itervox-linear-AbCdEf"]
  secrets_map = "LINEAR_API_KEY=itervox-linear"

  start_service        = true
  enable_cleanup_timer = true
}

output "dashboard_tunnel_command" {
  value = module.itervox.dashboard_tunnel_command
}
