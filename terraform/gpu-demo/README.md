# Meridian GPU Demo Cluster (Terraform)

This Terraform configuration provisions an on-demand, single-GPU Kubernetes node on AWS for the optional real-GPU demonstration of the Meridian control plane and vLLM inference serving.

---

> [!WARNING]
> **COST WARNING**: This configuration launches real GPU hardware. 
> - `g4dn.xlarge` (1x NVIDIA T4 16GB): ~$0.526 / hour on-demand.
> - `g5.xlarge` (1x NVIDIA A10G 24GB): ~$1.006 / hour on-demand.
> **Never leave this running unattended.** Always run `terraform destroy` immediately after testing.

---

## 1. Prerequisites
- [AWS CLI](https://aws.amazon.com/cli/) installed and configured with valid credentials (`aws configure`).
- [Terraform](https://www.terraform.io/) >= 1.5.0 installed.
- An SSH public key (e.g. `~/.ssh/id_rsa.pub`).

## 2. Step-by-Step Provisioning (Manual Execution Only)

### Step 1: Initialize Terraform
```bash
cd terraform/gpu-demo
terraform init
```

### Step 2: Plan and Review
Review the proposed changes and verify the instance type:
```bash
terraform plan -var="ssh_public_key=$(cat ~/.ssh/id_rsa.pub)"
```

### Step 3: Apply
```bash
terraform apply -var="ssh_public_key=$(cat ~/.ssh/id_rsa.pub)"
```
Wait approximately 3–5 minutes for the EC2 instance to initialize and run the cloud-init bootstrap script.

### Step 4: Retrieve Kubeconfig
```bash
# Output provides the scp command
terraform output kubeconfig_fetch_command
# Example:
scp ubuntu@<PUBLIC_IP>:~/.kube/config ./kubeconfig-gpu-demo
sed -i "s/127.0.0.1/<PUBLIC_IP>/g" ./kubeconfig-gpu-demo
export KUBECONFIG=$(pwd)/kubeconfig-gpu-demo
```

### Step 5: Verify Node and GPU Operator
```bash
kubectl get nodes -o wide
# Check Meridian topology labels
kubectl get node -o jsonpath='{.items[0].metadata.labels}' | jq .
# Verify NVIDIA GPU is visible to Kubernetes
kubectl get node -o jsonpath='{.items[0].status.allocatable}' | jq '."nvidia.com/gpu"'
```

### Step 6: Deploy Meridian Control Plane via Helm
```bash
helm install meridian charts/meridian \
  --namespace meridian-system \
  --create-namespace \
  --set agent.healthSource=nvml
```

### Step 7: Run Real-GPU Inference Benchmark
Deploy a real vLLM instance serving a small model (e.g. `Qwen/Qwen2.5-0.5B-Instruct`):
```bash
kubectl apply -f - <<EOF
apiVersion: meridian.dev/v1alpha1
kind: InferenceDeployment
metadata:
  name: demo-qwen
  namespace: default
spec:
  model:
    name: qwen-0.5b
    uri: "hf://Qwen/Qwen2.5-0.5B-Instruct"
  runtime:
    image: "vllm/vllm-openai:v0.9.2"
    port: 8000
    args:
      - "--model=Qwen/Qwen2.5-0.5B-Instruct"
      - "--gpu-memory-utilization=0.8"
  resources:
    gpusPerReplica: 1
    gpuType: "nvidia-t4"
  replicas: 1
EOF
```

Run the loadgen against the Meridian inference router:
```bash
kubectl port-forward svc/meridian-meridian-router 8080:8080 -n meridian-system &
go run ./tools/loadgen --router http://localhost:8080 --rate 5 --duration 60s
```

---

## 3. Mandatory Teardown (Destroy Target)

To prevent unexpected cloud costs, immediately tear down all infrastructure:

```bash
terraform destroy -var="ssh_public_key=$(cat ~/.ssh/id_rsa.pub)" -auto-approve
```

Verify in the AWS Console that the EC2 instance status is `terminated`.
