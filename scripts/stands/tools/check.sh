#!/usr/bin/env bash
# Stand "tools": the page Tools against a real router. A web server "web.tools.test:8080",
# an exit instance with a SOCKS5 inbound and the checked instance with outbounds HOST 1 and
# SOCKS5 1 (to the exit, with the Docker DNS of the exit as its DNS server) and the route
# "tools.test" to SOCKS5 1. Checks the address check (route, chain, DNS of every outbound,
# TCP connection, closed port, unknown name, URL), the ping (ICMP, TCP through the proxy, an
# absent address), the access level and the page in a headless browser; takes the pictures
# docs/images/tools-lookup.png and tools-ping.png.
# Usage: make stand-tools, or bash scripts/stands/tools/check.sh   (about 30 seconds)
# Needs the browser and the node modules of the screenshots (see scripts/screenshots.sh).
# Variables: CHROME, SCREENSHOT_TOOLS, SCREENSHOT_OUTPUT and those of ../lib.sh.
source "$(dirname "$0")/../lib.sh"
stand_require docker node npm
name="proximity-stand-tools"
exit_name="proximity-stand-tools-exit"
web="proximity-stand-tools-web"
tools="${SCREENSHOT_TOOLS:-$stand_root/../proximity-build/screenshots}"
mkdir -p "$tools"
cp -p "$stand_root"/scripts/screenshots/*.json "$stand_root"/scripts/screenshots/*.mjs "$tools/"
cp -p "$(dirname "$0")/check.mjs" "$tools/tools.mjs"
if [ ! "$tools/node_modules/.package-lock.json" -nt "$tools/package-lock.json" ]; then
    (cd "$tools" && npm ci --no-audit --no-fund)
fi
image="$(stand_image)"
stand_down "$name" "$exit_name" "$web"
stand_network_up
docker run -d --name "$web" --network "$stand_network" --network-alias web.tools.test \
    "${STAND_WEB_IMAGE:-python:3-alpine}" python3 -m http.server 8080 > /dev/null
stand_instance "$exit_name" "$image"
stand_instance "$name" "$image"
if [ "${STAND_KEEP:-}" != 1 ]; then
    trap 'stand_down "$name" "$exit_name" "$web"' EXIT
fi
STAND_LIB="$stand_root/scripts/stands/lib.mjs" \
    STAND_PICTURES="${SCREENSHOT_OUTPUT:-$stand_root/docs/images}" \
    node "$tools/tools.mjs" "$name" "$exit_name" "$(stand_address "$exit_name")" \
    "$(stand_address "$web")"
