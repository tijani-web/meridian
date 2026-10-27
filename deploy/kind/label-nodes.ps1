# deploy/kind/label-nodes.ps1
# Applies Meridian topology labels to kind worker nodes on Windows PowerShell.

param(
    [string]$Kubeconfig = "$PSScriptRoot\..\..\kubeconfig-meridian"
)

$ErrorActionPreference = "Stop"

if (Test-Path $Kubeconfig) {
    $env:KUBECONFIG = (Resolve-Path $Kubeconfig).Path
}

Write-Host "Applying topology labels to kind worker nodes..."

$workers = (kubectl get nodes --selector='!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[*].metadata.name}').Split(" ") | Where-Object { $_ -ne "" }

if ($workers.Count -lt 4) {
    Write-Warning "Expected at least 4 worker nodes, got $($workers.Count). Waiting for nodes..."
    Start-Sleep -Seconds 5
    $workers = (kubectl get nodes --selector='!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[*].metadata.name}').Split(" ") | Where-Object { $_ -ne "" }
}

if ($workers.Count -ge 1) {
    kubectl label node $workers[0] meridian.dev/nvlink-domain=domain-a meridian.dev/rack=rack-0 topology.kubernetes.io/zone=us-east-1a meridian.dev/gpu-type=nvidia-h100 --overwrite
}
if ($workers.Count -ge 2) {
    kubectl label node $workers[1] meridian.dev/nvlink-domain=domain-a meridian.dev/rack=rack-0 topology.kubernetes.io/zone=us-east-1a meridian.dev/gpu-type=nvidia-h100 --overwrite
}
if ($workers.Count -ge 3) {
    kubectl label node $workers[2] meridian.dev/nvlink-domain=domain-b meridian.dev/rack=rack-1 topology.kubernetes.io/zone=us-east-1b meridian.dev/gpu-type=nvidia-h100 --overwrite
}
if ($workers.Count -ge 4) {
    kubectl label node $workers[3] meridian.dev/nvlink-domain=domain-b meridian.dev/rack=rack-1 topology.kubernetes.io/zone=us-east-1b meridian.dev/gpu-type=nvidia-a100 --overwrite
}

Write-Host "Topology labels applied:"
kubectl get nodes -o custom-columns='NAME:.metadata.name,NVLINK:.metadata.labels.meridian\.dev/nvlink-domain,RACK:.metadata.labels.meridian\.dev/rack,ZONE:.metadata.labels.topology\.kubernetes\.io/zone,GPU:.metadata.labels.meridian\.dev/gpu-type'
