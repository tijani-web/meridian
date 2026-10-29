# generate-fleet.ps1 generates KWOK simulated GPU node manifests.
# Usage: .\generate-fleet.ps1 -Count 500 -OutFile nodes.yaml
param (
    [int]$Count = 500,
    [string]$OutFile = ""
)

$sb = [System.Text.StringBuilder]::new()
[void]$sb.AppendLine("# Generated simulated KWOK GPU fleet ($Count nodes)")
[void]$sb.AppendLine("# Every simulated result must be labeled 'simulated' per AGENTS.md §1.1")

for ($i = 1; $i -le $Count; $i++) {
    $index = "{0:D4}" -f $i
    $domain = [math]::Floor(($i - 1) / 8)
    $rack = [math]::Floor(($i - 1) / 32)
    $zone = if ($i % 2 -eq 0) { "us-east-1b" } else { "us-east-1a" }
    $model = if ($i % 4 -eq 0) { "nvidia-a100" } else { "nvidia-h100" }

    [void]$sb.AppendLine("---")
    [void]$sb.AppendLine("apiVersion: v1")
    [void]$sb.AppendLine("kind: Node")
    [void]$sb.AppendLine("metadata:")
    [void]$sb.AppendLine("  name: kwok-gpu-node-$index")
    [void]$sb.AppendLine("  annotations:")
    [void]$sb.AppendLine("    kwok.x-k8s.io/node: fake")
    [void]$sb.AppendLine("    node.alpha.kubernetes.io/ttl: `"0`"")
    [void]$sb.AppendLine("  labels:")
    [void]$sb.AppendLine("    type: kwok")
    [void]$sb.AppendLine("    meridian.dev/simulation: `"true`"")
    [void]$sb.AppendLine("    meridian.dev/gpu-model: $model")
    [void]$sb.AppendLine("    meridian.dev/nvlink-domain: domain-$domain")
    [void]$sb.AppendLine("    meridian.dev/rack: rack-$rack")
    [void]$sb.AppendLine("    topology.kubernetes.io/zone: $zone")
    [void]$sb.AppendLine("    kubernetes.io/hostname: kwok-gpu-node-$index")
    [void]$sb.AppendLine("    kubernetes.io/os: linux")
    [void]$sb.AppendLine("    kubernetes.io/arch: amd64")
    [void]$sb.AppendLine("spec:")
    [void]$sb.AppendLine("  taints: []")
    [void]$sb.AppendLine("status:")
    [void]$sb.AppendLine("  allocatable:")
    [void]$sb.AppendLine("    cpu: `"64`"")
    [void]$sb.AppendLine("    memory: `"512Gi`"")
    [void]$sb.AppendLine("    pods: `"110`"")
    [void]$sb.AppendLine("    nvidia.com/gpu: `"8`"")
    [void]$sb.AppendLine("  capacity:")
    [void]$sb.AppendLine("    cpu: `"64`"")
    [void]$sb.AppendLine("    memory: `"512Gi`"")
    [void]$sb.AppendLine("    pods: `"110`"")
    [void]$sb.AppendLine("    nvidia.com/gpu: `"8`"")
    [void]$sb.AppendLine("  conditions:")
    [void]$sb.AppendLine("    - type: Ready")
    [void]$sb.AppendLine("      status: `"True`"")
    [void]$sb.AppendLine("      reason: KwokNodeReady")
    [void]$sb.AppendLine("      message: KWOK simulated GPU node ready")
    [void]$sb.AppendLine("    - type: meridian.dev/GPUHealthy")
    [void]$sb.AppendLine("      status: `"True`"")
    [void]$sb.AppendLine("      reason: SimulatedHealthy")
    [void]$sb.AppendLine("      message: All 8 GPUs healthy")
    [void]$sb.AppendLine("  nodeInfo:")
    [void]$sb.AppendLine("    kubeletVersion: v1.33.1")
    [void]$sb.AppendLine("    operatingSystem: linux")
    [void]$sb.AppendLine("    architecture: amd64")
}

if ($OutFile -ne "") {
    [System.IO.File]::WriteAllText($OutFile, $sb.ToString())
    Write-Host "Generated $Count simulated KWOK nodes to $OutFile"
} else {
    Write-Output $sb.ToString()
}
