#!/usr/bin/env bash
# Takes the pictures of the outbound and inbound dialogs for docs/images.
# Usage: scripts/screenshots.sh [picture ...]
#   no arguments: all pictures; a picture is named as its file, ".png" may be omitted:
#   scripts/screenshots.sh outbound-monitoring inbound-users
# Builds the image "proximity-screenshots" from the working tree, starts a throwaway
# container "proximity-screenshots" (no host network, no capabilities, no volume, the UI
# on a free port of 127.0.0.1), fills it with the sample data of
# scripts/screenshots/seed.json through the API, opens every dialog in a headless browser
# and compares every picture with the saved one pixel by pixel: a file is written only
# when the pixels changed, so Git sees only real changes. The container is removed at the
# end; a running Proximity and its settings are not touched.
# Needs docker, node 22, npm and a browser on the Chromium engine: Chrome, Chromium, Brave,
# Thorium, Edge, Vivaldi or Yandex Browser. It is looked up by name in PATH, as "which"
# does, and then at the usual install paths.
# Variables:
#   PROXIMITY_IMAGE    use this ready image instead of building one
#   CHROME             name or path of the browser to use instead of the one found
#   SCREENSHOT_TOOLS   folder for node_modules (default ../proximity-build/screenshots)
#   SCREENSHOT_OUTPUT  folder for the pictures (default docs/images)
# NOTE: A picture that must have changed because the code or the layout of its dialog
# changed may be replaced right away: there is no need to open the new picture and to
# compare it with the old one. Look at a picture only when it is not clear whether it
# had to change.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tools="${SCREENSHOT_TOOLS:-$root/../proximity-build/screenshots}"
output="${SCREENSHOT_OUTPUT:-$root/docs/images}"
image="${PROXIMITY_IMAGE:-}"
container="proximity-screenshots"
user="admin"
password="Screenshots-1"
for tool in docker node npm; do
    if ! command -v "$tool" > /dev/null 2>&1; then
        echo "screenshots: $tool is required" >&2
        exit 1
    fi
done
mkdir -p "$tools"
cp -p "$root"/scripts/screenshots/package.json "$root"/scripts/screenshots/package-lock.json \
    "$root"/scripts/screenshots/screenshots.mjs "$root"/scripts/screenshots/seed.json "$tools/"
if [ ! "$tools/node_modules/.package-lock.json" -nt "$tools/package-lock.json" ]; then
    (cd "$tools" && npm ci --no-audit --no-fund)
fi
browser="$(node "$tools/screenshots.mjs" --browser)"
if [ -z "$image" ]; then
    image="proximity-screenshots"
    docker build -t "$image" "$root"
fi
docker rm -f -v "$container" > /dev/null 2>&1 || true
trap 'docker rm -f -v "$container" > /dev/null 2>&1 || true' EXIT
docker run -d --name "$container" -p 127.0.0.1::8899 -e PROXIMITY_ADMIN_USER="$user" \
    -e PROXIMITY_ADMIN_PASSWORD="$password" "$image" > /dev/null
address="$(docker port "$container" 8899/tcp | head -n 1)"
echo "screenshots: image is '$image', browser is '$browser', the UI is at http://$address," \
    "pictures go to '$output'"
CHROME="$browser" SCREENSHOT_USER="$user" SCREENSHOT_PASSWORD="$password" \
    node "$tools/screenshots.mjs" "http://$address" "$output" "$@"
