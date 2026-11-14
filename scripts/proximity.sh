#!/usr/bin/env bash
# Runs docker compose for Proximity with the per-OS configuration folder.
# Usage: scripts/proximity.sh up -d --build | down | logs -f | ps
set -euo pipefail
case "$(uname -s)" in
    Darwin) folder="$HOME/Library/Application Support/proximity" ;;
    MINGW* | MSYS* | CYGWIN*) folder="${APPDATA:-$HOME/AppData/Roaming}/proximity" ;;
    *) folder="${XDG_CONFIG_HOME:-$HOME/.config}/proximity" ;;
esac
export PROXIMITY_CONFIG_DIR="${PROXIMITY_CONFIG_DIR:-$folder}"
mkdir -p "$PROXIMITY_CONFIG_DIR"
cd "$(dirname "$0")/.."
file="docker-compose.yml"
if [ "${PROXIMITY_NETWORK:-bridge}" = "host" ]; then
    file="docker-compose.host.yml"
fi
if [ "$#" -eq 0 ]; then
    set -- up -d --build
fi
echo "proximity: config folder is '$PROXIMITY_CONFIG_DIR'"
exec docker compose -f "$file" "$@"
