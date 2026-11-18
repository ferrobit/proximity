#!/usr/bin/env bash
# Stand "reload": one instance on a fixed port of 127.0.0.1 and a headless browser with its
# page open. Checks that an open page warns that unsaved changes are lost and reloads itself
# after a restart of the container, also while a dialog is open.
# Usage: make stand-reload, or bash scripts/stands/reload/check.sh   (about 60 seconds)
# Needs the browser and the node modules of the screenshots (see scripts/screenshots.sh).
# Variables: STAND_PORT (default 18899), CHROME, SCREENSHOT_TOOLS and those of ../lib.sh.
source "$(dirname "$0")/../lib.sh"
stand_require docker node npm
name="proximity-stand-reload"
port="${STAND_PORT:-18899}"
tools="${SCREENSHOT_TOOLS:-$stand_root/../proximity-build/screenshots}"
mkdir -p "$tools"
cp -p "$stand_root"/scripts/screenshots/*.json "$stand_root"/scripts/screenshots/*.mjs "$tools/"
cp -p "$(dirname "$0")/check.mjs" "$tools/reload.mjs"
if [ ! "$tools/node_modules/.package-lock.json" -nt "$tools/package-lock.json" ]; then
    (cd "$tools" && npm ci --no-audit --no-fund)
fi
image="$(stand_image)"
stand_down "$name"
stand_network_up
stand_instance "$name" -p "127.0.0.1:$port:8899" "$image"
if [ "${STAND_KEEP:-}" != 1 ]; then
    trap 'stand_down "$name"' EXIT
fi
STAND_LIB="$stand_root/scripts/stands/lib.mjs" node "$tools/reload.mjs" "$name" "http://127.0.0.1:$port"
