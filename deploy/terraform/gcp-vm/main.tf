# itervox on one Compute Engine VM (CORE-106). Mirrors deploy/gcp/provision.sh:
# no public IP, IAP-only SSH, Cloud NAT egress, a separate persistent data disk
# for every state root (CORE-062), and first-boot bootstrap via bootstrap.sh.

locals {
  labels           = merge({ app = "itervox", managed-by = "opentofu" }, var.labels)
  network_tag      = "${var.name}-vm"
  data_device_name = "itervox-data"
  create_network   = var.network == ""
  network          = local.create_network ? google_compute_network.this[0].id : var.network
  subnetwork       = local.create_network ? google_compute_subnetwork.this[0].id : var.subnetwork

  startup_script = templatefile("${path.module}/../shared/startup.sh.tftpl", {
    itervox_version      = var.itervox_version
    repo_url             = var.repo_url
    data_disk_device     = "/dev/disk/by-id/google-${local.data_device_name}"
    install_root         = var.install_root
    start_service        = var.start_service
    enable_cleanup_timer = var.enable_cleanup_timer
    secrets_provider     = var.secrets_map == "" ? "" : "gcp"
    secrets_map          = var.secrets_map
    secrets_extra        = { ITERVOX_SECRETS_GCP_PROJECT = var.project_id }
  })
}

# ── identity: a dedicated service account with only what the VM uses ───────
resource "google_service_account" "vm" {
  project      = var.project_id
  account_id   = "${var.name}-vm"
  display_name = "itervox VM (${var.name})"
}

# Ops Agent log shipping and heartbeat-metrics.sh custom metrics.
resource "google_project_iam_member" "log_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.vm.email}"
}

resource "google_project_iam_member" "metric_writer" {
  project = var.project_id
  role    = "roles/monitoring.metricWriter"
  member  = "serviceAccount:${google_service_account.vm.email}"
}

# Secret access is granted per secret, never project-wide.
resource "google_secret_manager_secret_iam_member" "vm" {
  for_each  = toset(var.secret_ids)
  project   = var.project_id
  secret_id = each.value
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.vm.email}"
}

# ── network ─────────────────────────────────────────────────────────────────
resource "google_compute_network" "this" {
  count                   = local.create_network ? 1 : 0
  project                 = var.project_id
  name                    = "${var.name}-net"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "this" {
  count                    = local.create_network ? 1 : 0
  project                  = var.project_id
  name                     = "${var.name}-subnet"
  region                   = var.region
  network                  = google_compute_network.this[0].id
  ip_cidr_range            = var.subnet_cidr
  private_ip_google_access = true
}

resource "google_compute_firewall" "iap_ssh" {
  project       = var.project_id
  name          = "${var.name}-allow-iap-ssh"
  network       = local.network
  direction     = "INGRESS"
  source_ranges = var.iap_ssh_source_ranges
  target_tags   = [local.network_tag]
  description   = "SSH from IAP TCP forwarding only; the dashboard is forwarded over this session."

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

# Off unless dashboard_source_ranges is set: no public dashboard port by default.
resource "google_compute_firewall" "dashboard" {
  count         = length(var.dashboard_source_ranges) > 0 ? 1 : 0
  project       = var.project_id
  name          = "${var.name}-allow-dashboard"
  network       = local.network
  direction     = "INGRESS"
  source_ranges = var.dashboard_source_ranges
  target_tags   = [local.network_tag]

  allow {
    protocol = "tcp"
    ports    = [tostring(var.dashboard_port)]
  }
}

resource "google_compute_router" "this" {
  count   = var.create_nat ? 1 : 0
  project = var.project_id
  name    = "${var.name}-router"
  region  = var.region
  network = local.network
}

resource "google_compute_router_nat" "this" {
  count                              = var.create_nat ? 1 : 0
  project                            = var.project_id
  name                               = "${var.name}-nat"
  router                             = google_compute_router.this[0].name
  region                             = var.region
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"

  log_config {
    enable = true
    filter = "ERRORS_ONLY"
  }
}

# ── persistent data disk (CORE-062) ─────────────────────────────────────────
# A separate resource, not an instance boot/scratch disk: deleting or
# replacing the VM never deletes it. prevent_destroy makes `tofu destroy`
# (or a plan that would replace the disk) fail until an operator removes the
# guard on purpose.
resource "google_compute_disk" "data" {
  project = var.project_id
  name    = "${var.name}-data"
  zone    = var.zone
  type    = var.data_disk_type
  size    = var.data_disk_gb
  labels  = local.labels

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_resource_policy" "snapshots" {
  count   = var.snapshot_retention_days > 0 ? 1 : 0
  project = var.project_id
  name    = "${var.name}-data-daily"
  region  = var.region

  snapshot_schedule_policy {
    schedule {
      daily_schedule {
        days_in_cycle = 1
        start_time    = "04:00"
      }
    }
    retention_policy {
      max_retention_days    = var.snapshot_retention_days
      on_source_disk_delete = "KEEP_AUTO_SNAPSHOTS"
    }
    snapshot_properties {
      labels = local.labels
    }
  }
}

resource "google_compute_disk_resource_policy_attachment" "snapshots" {
  count   = var.snapshot_retention_days > 0 ? 1 : 0
  project = var.project_id
  name    = google_compute_resource_policy.snapshots[0].name
  disk    = google_compute_disk.data.name
  zone    = var.zone
}

# ── VM ──────────────────────────────────────────────────────────────────────
resource "google_compute_instance" "vm" {
  project                   = var.project_id
  name                      = var.name
  zone                      = var.zone
  machine_type              = var.machine_type
  tags                      = [local.network_tag]
  labels                    = local.labels
  deletion_protection       = var.deletion_protection
  allow_stopping_for_update = true

  boot_disk {
    auto_delete = true
    initialize_params {
      image = var.image
      size  = var.boot_disk_gb
      type  = "pd-balanced"
    }
  }

  # Stable path on the VM: /dev/disk/by-id/google-itervox-data (bootstrap.sh --data-disk).
  attached_disk {
    source      = google_compute_disk.data.id
    device_name = local.data_device_name
    mode        = "READ_WRITE"
  }

  network_interface {
    subnetwork = local.subnetwork

    dynamic "access_config" {
      for_each = var.public_ip ? [1] : []
      content {}
    }
  }

  service_account {
    email = google_service_account.vm.email
    # The scope is the OAuth ceiling; the IAM bindings above are the real limit.
    scopes = ["cloud-platform"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  metadata = {
    enable-oslogin         = "TRUE"
    block-project-ssh-keys = "TRUE"
    startup-script         = local.startup_script
  }
}
