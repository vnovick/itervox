output "instance_name" {
  description = "VM name."
  value       = google_compute_instance.vm.name
}

output "zone" {
  description = "VM zone."
  value       = google_compute_instance.vm.zone
}

output "service_account_email" {
  description = "The VM's service account (grant it access to further secrets here)."
  value       = google_service_account.vm.email
}

output "data_disk" {
  description = "Persistent data disk name and the stable device path bootstrap.sh mounts."
  value = {
    name   = google_compute_disk.data.name
    device = "/dev/disk/by-id/google-${local.data_device_name}"
  }
}

output "dashboard_tunnel_command" {
  description = "Reach the dashboard without opening a port: forward 8090 over IAP SSH, then open http://localhost:8090/?token=<ITERVOX_API_TOKEN>."
  value       = "gcloud compute ssh ${google_compute_instance.vm.name} --project ${var.project_id} --zone ${var.zone} --tunnel-through-iap -- -N -L 8090:localhost:8090"
}
