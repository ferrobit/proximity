#!/usr/bin/env bash
# Runs docker compose for Proximity with the per-OS configuration folder.
# Usage: scripts/proximity.sh up -d --build | stop | start | down | logs -f | ps
# Linux uses the host network (all host traffic is routed through Proximity);
# set PROXIMITY_NETWORK=bridge to get an isolated container with a plain proxy port.
# The variables in "saved" below are written to proximity.env in the configuration folder
# by "up" and reused by later runs, so a rebuilt container gets the same settings; set a
# variable again to change it. After a reboot Docker starts the container again when it
# was running (restart: unless-stopped) and Docker itself starts at boot.
set -euo pipefail
saved=(PROXIMITY_NETWORK PROXIMITY_UI_BIND PROXIMITY_PROXY_BIND PROXIMITY_INBOUND_BIND
    PROXIMITY_IPTABLES TZ)
case "$(uname -s)" in
    Darwin) system=macos; folder="$HOME/Library/Application Support/Proximity" ;;
    MINGW* | MSYS* | CYGWIN*) system=windows; folder="${APPDATA:-$HOME/AppData/Roaming}/Proximity" ;;
    *) system=linux; folder="${XDG_CONFIG_HOME:-$HOME/.config}/proximity" ;;
esac
export PROXIMITY_CONFIG_DIR="${PROXIMITY_CONFIG_DIR:-$folder}"
mkdir -p "$PROXIMITY_CONFIG_DIR"
settings="$PROXIMITY_CONFIG_DIR/proximity.env"
if [ -f "$settings" ]; then
    while IFS='=' read -r name value; do
        if [[ " ${saved[*]} " == *" $name "* ]] && [ -z "${!name+set}" ]; then
            export "$name=$value"
        fi
    done < "$settings"
fi
file="docker-compose.bridge.yml"
if [ "$system" = linux ] && [ "${PROXIMITY_NETWORK:-host}" = host ]; then
    file="docker-compose.yml"
fi
cd "$(dirname "$0")/.."
if [ "$#" -eq 0 ]; then
    set -- up -d --build
fi
if [ "$1" = up ]; then
    {
        echo "# Settings of 'scripts/proximity.sh up', reused by later runs; edit or delete to change."
        for name in "${saved[@]}"; do
            if [ -n "${!name:-}" ]; then
                echo "$name=${!name}"
            fi
        done
    } > "$settings.tmp"
    mv "$settings.tmp" "$settings"
    if [ "$system" != linux ]; then
        echo "proximity: to start the container after a reboot, turn on" \
            "'Start Docker Desktop when you sign in' in Docker Desktop"
    elif command -v systemctl > /dev/null 2>&1; then
        state="$(systemctl is-enabled docker.service 2> /dev/null || true)"
        if [ -n "$state" ] && [ "$state" != enabled ]; then
            echo "proximity: warning: Docker does not start at boot ($state), so the container" \
                "will not start after a reboot; run: sudo systemctl enable docker.service" >&2
        fi
    fi
fi
echo "proximity: config folder is '$PROXIMITY_CONFIG_DIR', compose file is '$file'"
exec docker compose -f "$file" "$@"
