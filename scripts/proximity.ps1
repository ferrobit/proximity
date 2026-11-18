# Runs docker compose for Proximity on Windows with the configuration folder in %APPDATA%.
# Usage: .\scripts\proximity.ps1 up -d --build | stop | start | down | logs -f | ps
# The variables in $saved are written to proximity.env in the configuration folder by "up"
# and reused by later runs, so a rebuilt container gets the same settings; set a variable
# again to change it. After a reboot Docker starts the container again when it was running
# (restart: unless-stopped) and Docker Desktop starts when you sign in.
$ErrorActionPreference = "Stop"
$saved = @("PROXIMITY_UI_BIND", "PROXIMITY_PROXY_BIND", "PROXIMITY_INBOUND_BIND", "TZ")
if (-not $env:PROXIMITY_CONFIG_DIR) {
    $env:PROXIMITY_CONFIG_DIR = Join-Path $env:APPDATA "Proximity"
}
New-Item -ItemType Directory -Force -Path $env:PROXIMITY_CONFIG_DIR | Out-Null
$settings = Join-Path $env:PROXIMITY_CONFIG_DIR "proximity.env"
if (Test-Path $settings) {
    foreach ($line in Get-Content $settings) {
        $name, $value = $line -split "=", 2
        if (($saved -contains $name) -and -not (Test-Path "env:$name")) {
            Set-Item -Path "env:$name" -Value $value
        }
    }
}
Set-Location (Join-Path $PSScriptRoot "..")
$arguments = $args
if ($arguments.Count -eq 0) {
    $arguments = @("up", "-d", "--build")
}
if ($arguments[0] -eq "up") {
    $lines = @("# Settings of 'scripts\proximity.ps1 up', reused by later runs; edit or delete to change.")
    foreach ($name in $saved) {
        $value = [Environment]::GetEnvironmentVariable($name)
        if ($value) {
            $lines += "$name=$value"
        }
    }
    Set-Content -Path $settings -Value $lines
    Write-Host "proximity: to start the container after a reboot, turn on 'Start Docker Desktop when you sign in' in Docker Desktop"
}
Write-Host "proximity: config folder is '$env:PROXIMITY_CONFIG_DIR'"
docker compose -f docker-compose.bridge.yml @arguments
