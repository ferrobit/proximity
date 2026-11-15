#!/usr/bin/env bash
# Runs docker compose for Proximity with the per-OS configuration folder.
# Usage: scripts/proximity.sh up -d --build | down | logs -f | ps
# Linux uses the host network (all host traffic is routed through Proximity);
# set PROXIMITY_NETWORK=bridge to get an isolated container with a plain proxy port.
set -euo pipefail
file="docker-compose.bridge.yml"
case "$(uname -s)" in
    Darwin) folder="$HOME/Library/Application Support/Proximity" ;;
    MINGW* | MSYS* | CYGWIN*) folder="${APPDATA:-$HOME/AppData/Roaming}/Proximity" ;;
    *)
        folder="${XDG_CONFIG_HOME:-$HOME/.config}/proximity"
        if [ "${PROXIMITY_NETWORK:-host}" = "host" ]; then
            file="docker-compose.yml"
        fi
        ;;
esac
export PROXIMITY_CONFIG_DIR="${PROXIMITY_CONFIG_DIR:-$folder}"
mkdir -p "$PROXIMITY_CONFIG_DIR"
cd "$(dirname "$0")/.."
if [ "$#" -eq 0 ]; then
    set -- up -d --build
fi
echo "proximity: config folder is '$PROXIMITY_CONFIG_DIR', compose file is '$file'"
exec docker compose -f "$file" "$@"
