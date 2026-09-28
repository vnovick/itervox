# itervox on one Azure VM (CORE-106). Mirrors deploy/azure/provision.sh: no
# public IP, NAT gateway egress, no inbound NSG allow rule, a system-assigned
# identity with Monitoring Metrics Publisher, a separate managed data disk for
# every state root (CORE-062) and first-boot bootstrap via bootstrap.sh.

locals {
  tags     = merge({ app = "itervox", managed-by = "opentofu" }, var.tags)
  rg_name  = var.create_resource_group ? azurerm_resource_group.this[0].name : data.azurerm_resource_group.this[0].name
  rg_id    = var.create_resource_group ? azurerm_resource_group.this[0].id : data.azurerm_resource_group.this[0].id
  location = var.location

  startup_script = templatefile("${path.module}/../shared/startup.sh.tftpl", {
    itervox_version = var.itervox_version
    repo_url        = var.repo_url
    # LUN 0 -> stable udev link; never /dev/sdb (the ephemeral resource disk).
    data_disk_device     = "/dev/disk/azure/scsi1/lun0"
    install_root         = var.install_root
    start_service        = var.start_service
    enable_cleanup_timer = var.enable_cleanup_timer
    secrets_provider     = var.secrets_map == "" ? "" : "azure"
    secrets_map          = var.secrets_map
    secrets_extra        = { ITERVOX_SECRETS_AZURE_VAULT = var.key_vault_name }
  })
}

resource "azurerm_resource_group" "this" {
  count    = var.create_resource_group ? 1 : 0
  name     = var.resource_group_name
  location = var.location
  tags     = local.tags
}

data "azurerm_resource_group" "this" {
  count = var.create_resource_group ? 0 : 1
  name  = var.resource_group_name
}

# ── network ─────────────────────────────────────────────────────────────────
resource "azurerm_virtual_network" "this" {
  name                = "${var.name}-vnet"
  resource_group_name = local.rg_name
  location            = local.location
  address_space       = [var.vnet_cidr]
  tags                = local.tags
}

resource "azurerm_subnet" "vm" {
  name                 = "${var.name}-subnet"
  resource_group_name  = local.rg_name
  virtual_network_name = azurerm_virtual_network.this.name
  address_prefixes     = [var.subnet_cidr]
  # No implicit outbound: egress is the NAT gateway below.
  default_outbound_access_enabled = false
}

# No inbound allow rule by default: the platform's DenyAllInBound applies.
resource "azurerm_network_security_group" "vm" {
  name                = "${var.name}-nsg"
  resource_group_name = local.rg_name
  location            = local.location
  tags                = local.tags

  dynamic "security_rule" {
    for_each = length(var.dashboard_source_prefixes) > 0 ? [1] : []
    content {
      name                       = "allow-itervox-dashboard"
      priority                   = 200
      direction                  = "Inbound"
      access                     = "Allow"
      protocol                   = "Tcp"
      source_port_range          = "*"
      destination_port_range     = tostring(var.dashboard_port)
      source_address_prefixes    = var.dashboard_source_prefixes
      destination_address_prefix = "*"
    }
  }
}

resource "azurerm_subnet_network_security_group_association" "vm" {
  subnet_id                 = azurerm_subnet.vm.id
  network_security_group_id = azurerm_network_security_group.vm.id
}

resource "azurerm_public_ip" "nat" {
  count               = var.create_nat ? 1 : 0
  name                = "${var.name}-nat-ip"
  resource_group_name = local.rg_name
  location            = local.location
  allocation_method   = "Static"
  sku                 = "Standard"
  tags                = local.tags
}

resource "azurerm_nat_gateway" "this" {
  count               = var.create_nat ? 1 : 0
  name                = "${var.name}-natgw"
  resource_group_name = local.rg_name
  location            = local.location
  sku_name            = "Standard"
  tags                = local.tags
}

resource "azurerm_nat_gateway_public_ip_association" "this" {
  count                = var.create_nat ? 1 : 0
  nat_gateway_id       = azurerm_nat_gateway.this[0].id
  public_ip_address_id = azurerm_public_ip.nat[0].id
}

resource "azurerm_subnet_nat_gateway_association" "this" {
  count          = var.create_nat ? 1 : 0
  subnet_id      = azurerm_subnet.vm.id
  nat_gateway_id = azurerm_nat_gateway.this[0].id
}

resource "azurerm_network_interface" "vm" {
  name                = "${var.name}-nic"
  resource_group_name = local.rg_name
  location            = local.location
  tags                = local.tags

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.vm.id
    private_ip_address_allocation = "Dynamic"
  }
}

# ── VM ──────────────────────────────────────────────────────────────────────
resource "azurerm_linux_virtual_machine" "vm" {
  name                            = var.name
  resource_group_name             = local.rg_name
  location                        = local.location
  size                            = var.vm_size
  admin_username                  = var.admin_username
  disable_password_authentication = true
  network_interface_ids           = [azurerm_network_interface.vm.id]
  # cloud-init runs a "#!" custom_data as a first-boot script.
  custom_data = base64encode(local.startup_script)
  tags        = local.tags

  admin_ssh_key {
    username   = var.admin_username
    public_key = var.admin_ssh_public_key
  }

  identity {
    type = "SystemAssigned"
  }

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Premium_LRS"
    disk_size_gb         = var.os_disk_gb
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "ubuntu-24_04-lts"
    sku       = "server"
    version   = "latest"
  }

  lifecycle {
    ignore_changes = [custom_data]
  }
}

# ── least-privilege identity ────────────────────────────────────────────────
# Same grant provision.sh makes (Azure Monitor agent custom metrics), scoped to
# the VM itself.
resource "azurerm_role_assignment" "metrics_publisher" {
  scope                = azurerm_linux_virtual_machine.vm.id
  role_definition_name = "Monitoring Metrics Publisher"
  principal_id         = azurerm_linux_virtual_machine.vm.identity[0].principal_id
}

resource "azurerm_role_assignment" "key_vault_secrets" {
  count                = var.key_vault_id != "" ? 1 : 0
  scope                = var.key_vault_id
  role_definition_name = "Key Vault Secrets User"
  principal_id         = azurerm_linux_virtual_machine.vm.identity[0].principal_id
}

# ── persistent data disk (CORE-062) ─────────────────────────────────────────
# A standalone managed disk: deleting the VM detaches it, never deletes it.
resource "azurerm_managed_disk" "data" {
  name                 = "${var.name}-data"
  resource_group_name  = local.rg_name
  location             = local.location
  storage_account_type = var.data_disk_sku
  create_option        = "Empty"
  disk_size_gb         = var.data_disk_gb
  tags                 = local.tags

  lifecycle {
    prevent_destroy = true
  }
}

resource "azurerm_management_lock" "data" {
  count      = var.data_disk_lock ? 1 : 0
  name       = "${var.name}-data-cannot-delete"
  scope      = azurerm_managed_disk.data.id
  lock_level = "CanNotDelete"
  notes      = "itervox state (HOME, ledgers, worktrees). Remove this lock on purpose before deleting."
}

resource "azurerm_virtual_machine_data_disk_attachment" "data" {
  managed_disk_id    = azurerm_managed_disk.data.id
  virtual_machine_id = azurerm_linux_virtual_machine.vm.id
  lun                = 0
  caching            = "ReadWrite"
}

# ── retention: Azure Backup for the data disk ───────────────────────────────
resource "azurerm_data_protection_backup_vault" "this" {
  count               = var.backup_retention_days > 0 ? 1 : 0
  name                = "${var.name}-backup"
  resource_group_name = local.rg_name
  location            = local.location
  datastore_type      = "OperationalStore"
  redundancy          = "LocallyRedundant"
  tags                = local.tags

  identity {
    type = "SystemAssigned"
  }
}

resource "azurerm_role_assignment" "backup_disk_reader" {
  count                = var.backup_retention_days > 0 ? 1 : 0
  scope                = azurerm_managed_disk.data.id
  role_definition_name = "Disk Backup Reader"
  principal_id         = azurerm_data_protection_backup_vault.this[0].identity[0].principal_id
}

resource "azurerm_role_assignment" "backup_snapshot_contributor" {
  count                = var.backup_retention_days > 0 ? 1 : 0
  scope                = local.rg_id
  role_definition_name = "Disk Snapshot Contributor"
  principal_id         = azurerm_data_protection_backup_vault.this[0].identity[0].principal_id
}

resource "azurerm_data_protection_backup_policy_disk" "daily" {
  count                           = var.backup_retention_days > 0 ? 1 : 0
  name                            = "${var.name}-data-daily"
  vault_id                        = azurerm_data_protection_backup_vault.this[0].id
  backup_repeating_time_intervals = ["R/2025-01-01T04:00:00+00:00/P1D"]
  default_retention_duration      = "P${var.backup_retention_days}D"
  time_zone                       = "UTC"
}

resource "azurerm_data_protection_backup_instance_disk" "data" {
  count                        = var.backup_retention_days > 0 ? 1 : 0
  name                         = "${var.name}-data"
  location                     = local.location
  vault_id                     = azurerm_data_protection_backup_vault.this[0].id
  disk_id                      = azurerm_managed_disk.data.id
  snapshot_resource_group_name = local.rg_name
  backup_policy_id             = azurerm_data_protection_backup_policy_disk.daily[0].id

  depends_on = [
    azurerm_role_assignment.backup_disk_reader,
    azurerm_role_assignment.backup_snapshot_contributor,
  ]
}
