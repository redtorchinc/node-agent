# uninstall.ps1 — remove rt-node-agent on Windows.
# Run from elevated PowerShell.

$ErrorActionPreference = 'Stop'

$Binary = 'C:\Program Files\RedTorch\rt-node-agent.exe'

if (-not (Test-Path $Binary)) {
    Write-Host "uninstall.ps1: $Binary not found; nothing to do"
    exit 0
}

& $Binary uninstall
Remove-Item -Force $Binary -ErrorAction SilentlyContinue

Write-Host "rt-node-agent removed."
