variable "aws_region" {
  description = "AWS region for the GPU demo instance"
  type        = string
  default     = "us-east-1"
}

variable "instance_type" {
  description = "GPU instance type (e.g. g4dn.xlarge with 1x NVIDIA T4, or g5.xlarge with 1x NVIDIA A10G)"
  type        = string
  default     = "g4dn.xlarge"
}

variable "cluster_name" {
  description = "Identifier name for Meridian GPU demo resources"
  type        = string
  default     = "meridian-gpu-demo"
}

variable "allowed_cidr" {
  description = "IP CIDR permitted to access SSH and Kubernetes API"
  type        = string
  default     = "0.0.0.0/0"
}

variable "ssh_public_key" {
  description = "SSH public key to provision for ubuntu user access"
  type        = string
  default     = ""
}
