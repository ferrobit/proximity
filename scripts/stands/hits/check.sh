#!/usr/bin/env bash
# Stand "hits": one instance with its proxy port published and a headless browser. Sends
# requests through the router and checks the hit counters of the routes: the count per route
# and for "Everything else", the history samples, no loss and no double count across a reload
# of the router configuration, the counters after a restart of the container, the chart and
# the column of the Routes page, the reset.
# Usage: make stand-hits, or bash scripts/stands/hits/check.sh   (about 30 seconds)
# Needs the browser and the node modules of the screenshots (see scripts/screenshots.sh).
# Variables: CHROME, SCREENSHOT_TOOLS and those of ../lib.sh.
source "$(dirname "$0")/../lib.sh"
stand_require docker node npm
name="proximity-stand-hits"
tools="${SCREENSHOT_TOOLS:-$stand_root/../proximity-build/screenshots}"
mkdir -p "$tools"
cp -p "$stand_root"/scripts/screenshots/*.json "$stand_root"/scripts/screenshots/*.mjs "$tools/"
cp -p "$(dirname "$0")/check.mjs" "$tools/hits.mjs"
if [ ! "$tools/node_modules/.package-lock.json" -nt "$tools/package-lock.json" ]; then
    (cd "$tools" && npm ci --no-audit --no-fund)
fi
image="$(stand_image)"
stand_down "$name"
stand_network_up
stand_instance "$name" -p 127.0.0.1::7899 "$image"
if [ "${STAND_KEEP:-}" != 1 ]; then
    trap 'stand_down "$name"' EXIT
fi
STAND_LIB="$stand_root/scripts/stands/lib.mjs" node "$tools/hits.mjs" "$name"
