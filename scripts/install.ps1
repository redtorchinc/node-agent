# install.ps1 — Windows bootstrap for rt-node-agent.
#
# Usage (from elevated PowerShell):
#   iwr -useb https://github.com/redtorchinc/node-agent/releases/latest/download/install.ps1 | iex
#
# Idempotent. Requires Administrator (the SCM register step will not proceed otherwise).

$ErrorActionPreference = 'Stop'

$Repo      = 'redtorchinc/node-agent'
$Binary    = 'rt-node-agent.exe'
$Version   = if ($env:RT_AGENT_VERSION) { $env:RT_AGENT_VERSION } else { 'latest' }
$InstallDir = if ($env:RT_AGENT_INSTALL_DIR) { $env:RT_AGENT_INSTALL_DIR } else { 'C:\Program Files\RedTorch' }

function Fail($msg) { Write-Error "install.ps1: $msg"; exit 1 }
function Say($msg)  { Write-Host "install.ps1: $msg" }

# --- admin check ---
$adminRole = [Security.Principal.WindowsBuiltinRole]::Administrator
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole($adminRole)) {
    Fail 'must run from an elevated PowerShell (Right-click → Run as Administrator)'
}

# --- detect arch ---
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    default { Fail "unsupported arch: $($env:PROCESSOR_ARCHITECTURE)" }
}

$asset = "rt-node-agent_windows_${arch}.exe"
if ($Version -eq 'latest') {
    $url = "https://github.com/$Repo/releases/latest/download/$asset"
} else {
    $url = "https://github.com/$Repo/releases/download/$Version/$asset"
}

# --- download ---
$tmp = New-Item -ItemType Directory -Path ([System.IO.Path]::GetTempPath()) -Name ([System.Guid]::NewGuid().ToString()) -Force
try {
    Say "downloading $asset"
    $dest = Join-Path $tmp.FullName $asset
    Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing

    # TODO(M11): minisign verification on Windows once a pubkey is pinned.

    # --- install binary ---
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $target = Join-Path $InstallDir $Binary
    Copy-Item -Force $dest $target
    Say "installed to $target"

    # --- register service ---
    & $target install
    if ($LASTEXITCODE -ne 0) { Fail 'rt-node-agent install failed' }

    # --- healthcheck ---
    Start-Sleep -Seconds 1
    $port = if ($env:RT_AGENT_PORT) { $env:RT_AGENT_PORT } else { '11435' }
    try {
        $resp = Invoke-WebRequest -Uri "http://127.0.0.1:$port/version" -UseBasicParsing -TimeoutSec 3
        Say "rt-node-agent running on port $port"
    } catch {
        Fail "rt-node-agent did not respond on port $port; check Event Viewer"
    }

    Say "done. /health: http://127.0.0.1:$port/health"
    Say "next: write a token to %ProgramData%\rt-node-agent\token to enable /actions/*"
} finally {
    Remove-Item -Recurse -Force $tmp.FullName -ErrorAction SilentlyContinue
}
