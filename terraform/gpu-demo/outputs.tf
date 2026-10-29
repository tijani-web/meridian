output "public_ip" {
  description = "Public IP address of the GPU demo instance"
  value       = aws_instance.gpu_node.public_ip
}

output "ssh_command" {
  description = "SSH command to connect to the GPU node"
  value       = "ssh ubuntu@${aws_instance.gpu_node.public_ip}"
}

output "kubeconfig_fetch_command" {
  description = "Command to fetch kubeconfig from the remote GPU instance"
  value       = "scp ubuntu@${aws_instance.gpu_node.public_ip}:~/.kube/config ./kubeconfig-gpu-demo"
}
