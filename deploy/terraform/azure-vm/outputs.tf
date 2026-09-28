output "vm_id" {
  description = "VM resource id (the Bastion tunnel target)."
  value       = azurerm_linux_virtual_machine.vm.id
}

output "principal_id" {
  description = "The VM's system-assigned identity (grant it access to further secrets here)."
  value       = azurerm_linux_virtual_machine.vm.identity[0].principal_id
}

output "data_disk" {
  description = "Managed data disk id and the stable device path bootstrap.sh mounts."
  value = {
    id     = azurerm_managed_disk.data.id
    device = "/dev/disk/azure/scsi1/lun0"
  }
}

output "private_ip" {
  description = "Private address of the VM (reach it over Bastion or Tailscale; forward 8090 over SSH)."
  value       = azurerm_network_interface.vm.private_ip_address
}
