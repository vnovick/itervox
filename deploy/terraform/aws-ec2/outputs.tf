output "instance_id" {
  description = "EC2 instance id."
  value       = aws_instance.this.id
}

output "instance_role_name" {
  description = "IAM role of the instance (attach further least-privilege policies here)."
  value       = aws_iam_role.instance.name
}

output "data_volume" {
  description = "Persistent data volume id and the stable device path bootstrap.sh mounts."
  value = {
    id     = aws_ebs_volume.data.id
    device = local.data_device
  }
}

output "dashboard_tunnel_command" {
  description = "Reach the dashboard without an inbound rule: SSM port forwarding, then open http://localhost:8090/?token=<ITERVOX_API_TOKEN>."
  value       = "aws ssm start-session --target ${aws_instance.this.id} --document-name AWS-StartPortForwardingSession --parameters '{\"portNumber\":[\"8090\"],\"localPortNumber\":[\"8090\"]}'"
}
