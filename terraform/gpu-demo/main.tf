terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

# ============================================================
# VPC & Networking
# ============================================================
resource "aws_vpc" "demo" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true
  enable_dns_support   = true

  tags = {
    Name    = "${var.cluster_name}-vpc"
    Project = "Meridian"
  }
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.demo.id
  cidr_block              = "10.0.1.0/24"
  map_public_ip_on_launch = true
  availability_zone       = "${var.aws_region}a"

  tags = {
    Name    = "${var.cluster_name}-subnet"
    Project = "Meridian"
  }
}

resource "aws_internet_gateway" "gw" {
  vpc_id = aws_vpc.demo.id

  tags = {
    Name    = "${var.cluster_name}-igw"
    Project = "Meridian"
  }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.demo.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.gw.id
  }

  tags = {
    Name    = "${var.cluster_name}-rt"
    Project = "Meridian"
  }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

# ============================================================
# Security Group
# ============================================================
resource "aws_security_group" "gpu_node" {
  name        = "${var.cluster_name}-sg"
  description = "Security group for Meridian GPU demo instance"
  vpc_id      = aws_vpc.demo.id

  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.allowed_cidr]
  }

  ingress {
    description = "K8s API"
    from_port   = 6443
    to_port     = 6443
    protocol    = "tcp"
    cidr_blocks = [var.allowed_cidr]
  }

  ingress {
    description = "Meridian Router"
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = [var.allowed_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name    = "${var.cluster_name}-sg"
    Project = "Meridian"
  }
}

# ============================================================
# Deep Learning AMI lookup
# ============================================================
data "aws_ami" "ubuntu_gpu" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["Deep Learning OSS Nvidia Driver AMI GPU PyTorch * (Ubuntu 22.04)*"]
  }

  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
}

# ============================================================
# GPU Demo Instance
# ============================================================
resource "aws_key_pair" "demo" {
  count      = var.ssh_public_key != "" ? 1 : 0
  key_name   = "${var.cluster_name}-key"
  public_key = var.ssh_public_key
}

resource "aws_instance" "gpu_node" {
  ami                    = data.aws_ami.ubuntu_gpu.id
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.gpu_node.id]
  key_name               = var.ssh_public_key != "" ? aws_key_pair.demo[0].key_name : null

  root_block_device {
    volume_size           = 100
    volume_type           = "gp3"
    delete_on_termination = true
  }

  user_data = <<-EOF
              #!/bin/bash
              set -euxo pipefail

              # Update and install Docker and prerequisites
              apt-get update -y
              apt-get install -y apt-transport-https ca-certificates curl jq

              # Install NVIDIA Container Toolkit
              curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
              curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
                sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
                tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
              apt-get update -y
              apt-get install -y nvidia-container-toolkit

              # Install k3s with nvidia container runtime enabled
              curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--default-runtime=nvidia" sh -

              # Copy kubeconfig for user ubuntu
              mkdir -p /home/ubuntu/.kube
              cp /etc/rancher/k3s/k3s.yaml /home/ubuntu/.kube/config
              chown -R ubuntu:ubuntu /home/ubuntu/.kube
              chmod 600 /home/ubuntu/.kube/config

              # Label GPU node with Meridian topology labels
              kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml label node $(hostname) \
                meridian.dev/gpu-type=nvidia-t4 \
                meridian.dev/nvlink-domain=nvlink-domain-0 \
                meridian.dev/rack=rack-01 \
                topology.kubernetes.io/zone=us-east-1a --overwrite
              EOF

  tags = {
    Name    = "${var.cluster_name}-node"
    Project = "Meridian"
  }
}
