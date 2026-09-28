# itervox on one EC2 instance (CORE-106). Mirrors deploy/aws/provision.sh:
# no inbound rule (SSM Session Manager only), IMDSv2, an SSM + CloudWatch
# instance role, a separate persistent data volume for every state root
# (CORE-062) and first-boot bootstrap via bootstrap.sh.

data "aws_region" "current" {}
data "aws_partition" "current" {}

data "aws_subnet" "this" {
  id = var.subnet_id
}

locals {
  tags = merge({ Name = var.name, app = "itervox", managed-by = "opentofu" }, var.tags)

  # Graviton families put a "g" right after the generation digit (t4g, m7g,
  # r6gd); everything else is x86_64. Same rule as provision.sh.
  family   = split(".", var.instance_type)[0]
  ami_arch = can(regex("^[a-z]+[0-9]+g", local.family)) ? "arm64" : "x86_64"
  ami_id   = var.ami_id != "" ? var.ami_id : data.aws_ssm_parameter.al2023[0].value

  backup_tag_key = "itervox-backup"

  # Nitro exposes EBS as NVMe; the by-id link embeds the volume id without its dash.
  data_device = "/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_${replace(aws_ebs_volume.data.id, "-", "")}"

  startup_script = templatefile("${path.module}/../shared/startup.sh.tftpl", {
    itervox_version      = var.itervox_version
    repo_url             = var.repo_url
    data_disk_device     = local.data_device
    install_root         = var.install_root
    start_service        = var.start_service
    enable_cleanup_timer = var.enable_cleanup_timer
    secrets_provider     = var.secrets_map == "" ? "" : "aws"
    secrets_map          = var.secrets_map
    secrets_extra        = { ITERVOX_SECRETS_AWS_REGION = data.aws_region.current.region }
  })
}

data "aws_ssm_parameter" "al2023" {
  count = var.ami_id == "" ? 1 : 0
  name  = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-${local.ami_arch}"
}

# ── identity ────────────────────────────────────────────────────────────────
data "aws_iam_policy_document" "assume_ec2" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "instance" {
  name               = "${var.name}-instance"
  assume_role_policy = data.aws_iam_policy_document.assume_ec2.json
  tags               = local.tags
}

# Session Manager (the only way in) and the CloudWatch agent (logs + metrics),
# exactly what provision.sh attaches.
resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.instance.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_role_policy_attachment" "cloudwatch" {
  role       = aws_iam_role.instance.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/CloudWatchAgentServerPolicy"
}

data "aws_iam_policy_document" "secrets" {
  count = length(var.secret_arns) > 0 ? 1 : 0
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = var.secret_arns
  }
}

resource "aws_iam_role_policy" "secrets" {
  count  = length(var.secret_arns) > 0 ? 1 : 0
  name   = "read-itervox-secrets"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.secrets[0].json
}

resource "aws_iam_instance_profile" "instance" {
  name = "${var.name}-instance"
  role = aws_iam_role.instance.name
  tags = local.tags
}

# ── network: egress only ────────────────────────────────────────────────────
resource "aws_security_group" "instance" {
  name        = "${var.name}-sg"
  description = "itervox - egress only, access via SSM"
  vpc_id      = var.vpc_id
  tags        = local.tags
}

resource "aws_vpc_security_group_egress_rule" "all_v4" {
  security_group_id = aws_security_group.instance.id
  description       = "tracker, GitHub, model APIs, package mirrors"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_ingress_rule" "dashboard" {
  for_each          = toset(var.dashboard_cidr_blocks)
  security_group_id = aws_security_group.instance.id
  description       = "itervox dashboard (opt-in)"
  ip_protocol       = "tcp"
  from_port         = var.dashboard_port
  to_port           = var.dashboard_port
  cidr_ipv4         = each.value
}

# ── persistent data volume (CORE-062) ───────────────────────────────────────
# Separate from the instance: terminating or replacing the instance never
# deletes it (an attachment made after launch is DeleteOnTermination=false).
# prevent_destroy makes `tofu destroy` fail until the guard is removed on
# purpose, and final_snapshot keeps a last snapshot if it is ever deleted.
resource "aws_ebs_volume" "data" {
  availability_zone = data.aws_subnet.this.availability_zone
  size              = var.data_volume_gb
  type              = var.data_volume_type
  encrypted         = true
  kms_key_id        = var.kms_key_id != "" ? var.kms_key_id : null
  final_snapshot    = true
  tags              = merge(local.tags, { Name = "${var.name}-data", (local.backup_tag_key) = var.name })

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_volume_attachment" "data" {
  device_name                    = "/dev/sdf"
  volume_id                      = aws_ebs_volume.data.id
  instance_id                    = aws_instance.this.id
  stop_instance_before_detaching = true
}

# Daily snapshots, count-based retention (Data Lifecycle Manager).
data "aws_iam_policy_document" "assume_dlm" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["dlm.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dlm" {
  count              = var.snapshot_retention_days > 0 ? 1 : 0
  name               = "${var.name}-dlm"
  assume_role_policy = data.aws_iam_policy_document.assume_dlm.json
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "dlm" {
  count      = var.snapshot_retention_days > 0 ? 1 : 0
  role       = aws_iam_role.dlm[0].name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"
}

resource "aws_dlm_lifecycle_policy" "data" {
  count              = var.snapshot_retention_days > 0 ? 1 : 0
  description        = "itervox ${var.name} data volume daily snapshots"
  execution_role_arn = aws_iam_role.dlm[0].arn
  state              = "ENABLED"
  tags               = local.tags

  policy_details {
    resource_types = ["VOLUME"]
    target_tags    = { (local.backup_tag_key) = var.name }

    schedule {
      name = "daily"
      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = ["04:00"]
      }
      retain_rule {
        count = var.snapshot_retention_days
      }
      copy_tags = true
    }
  }
}

# ── instance ────────────────────────────────────────────────────────────────
resource "aws_instance" "this" {
  ami                         = local.ami_id
  instance_type               = var.instance_type
  subnet_id                   = var.subnet_id
  vpc_security_group_ids      = [aws_security_group.instance.id]
  iam_instance_profile        = aws_iam_instance_profile.instance.name
  associate_public_ip_address = var.associate_public_ip
  disable_api_termination     = var.deletion_protection
  user_data                   = local.startup_script
  # A changed bootstrap script must not replace the instance; it runs once.
  user_data_replace_on_change = false
  tags                        = local.tags

  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  root_block_device {
    volume_size = var.root_volume_gb
    volume_type = "gp3"
    encrypted   = true
    kms_key_id  = var.kms_key_id != "" ? var.kms_key_id : null
  }

  lifecycle {
    ignore_changes = [ami, user_data]
  }
}
