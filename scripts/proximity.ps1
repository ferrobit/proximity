# Runs docker compose for Proximity on Windows with the configuration folder in %APPDATA%.
# Usage: .\scripts\proximity.ps1 up -d --build | down | logs -f | ps
$ErrorActionPreference = "Stop"
if (-not $env:PROXIMITY_CONFIG_DIR) {
    $env:PROXIMITY_CONFIG_DIR = Join-Path $env:APPDATA "Proximity"
}
New-Item -ItemType Directory -Force -Path $env:PROXIMITY_CONFIG_DIR | Out-Null
Set-Location (Join-Path $PSScriptRoot "..")
$arguments = $args
if ($arguments.Count -eq 0) {
    $arguments = @("up", "-d", "--build")
}
Write-Host "proximity: config folder is '$env:PROXIMITY_CONFIG_DIR'"
docker compose -f docker-compose.bridge.yml @arguments
