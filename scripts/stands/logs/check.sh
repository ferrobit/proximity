#!/usr/bin/env bash
# Stand "logs": one instance and a headless browser. Checks the button "Clear" of the log
# window of an outbound and of an inbound, and the setting "Clear the log when a connection
# is started or restarted": on (default) Start and Restart empty the log, off they keep it;
# the restart of the container never clears a log; a cleared log stays empty after it.
# Usage: make stand-logs, or bash scripts/stands/logs/check.sh   (about 40 seconds)
# Needs the browser and the node modules of the screenshots (see scripts/screenshots.sh).
# Variables: CHROME, SCREENSHOT_TOOLS and those of ../lib.sh.
source "$(dirname "$0")/../lib.sh"
stand_require docker node npm
name="proximity-stand-logs"
tools="${SCREENSHOT_TOOLS:-$stand_root/../proximity-build/screenshots}"
mkdir -p "$tools"
cp -p "$stand_root"/scripts/screenshots/*.json "$stand_root"/scripts/screenshots/*.mjs "$tools/"
cp -p "$(dirname "$0")/check.mjs" "$tools/logs.mjs"
if [ ! "$tools/node_modules/.package-lock.json" -nt "$tools/package-lock.json" ]; then
    (cd "$tools" && npm ci --no-audit --no-fund)
fi
image="$(stand_image)"
stand_down "$name"
stand_network_up
stand_instance "$name" "$image"
if [ "${STAND_KEEP:-}" != 1 ]; then
    trap 'stand_down "$name"' EXIT
fi
STAND_LIB="$stand_root/scripts/stands/lib.mjs" node "$tools/logs.mjs" "$name"
