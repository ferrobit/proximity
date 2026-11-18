#!/usr/bin/env bash
# Takes the pictures of the web UI for docs/images: all pictures of the documentation.
# Usage: scripts/screenshots.sh [picture ...]
#   no arguments: all pictures; a picture is named as its file, ".png" may be omitted;
#   "static" and "live" name the two groups of pictures:
#   scripts/screenshots.sh outbound-monitoring inbound-users
#   scripts/screenshots.sh static
# Builds the image "proximity-screenshots" from the working tree and starts a throwaway
# stand in its own Docker network: the container "proximity-screenshots" (the instance in
# the pictures) and "proximity-screenshots-peer" (a second instance that plays the far
# side: SOCKS5 and HTTP proxies, an OpenVPN server and the sites). Both get the sample
# data of scripts/screenshots/seed.json through the API, traffic is sent through the
# proxy ports, and a headless browser opens every page and dialog. Every picture is
# compared with the saved one pixel by pixel: a file is written only when the pixels
# changed, so Git sees only real changes. Static pictures (dialogs) give the same pixels
# in every run; live pictures (states, traffic, time, logs) differ every time. The stand
# is removed at the end; a running Proximity and its settings are not touched, the stand
# does not use the host network and sends nothing to the Internet.
# Needs docker with /dev/net/tun, node 22, npm and a browser on the Chromium engine:
# Chrome, Chromium, Brave, Thorium, Edge, Vivaldi or Yandex Browser. It is looked up by
# name in PATH, as "which" does, and then at the usual install paths.
# Variables:
#   PROXIMITY_IMAGE     use this ready image instead of building one
#   CHROME              name or path of the browser to use instead of the one found
#   SCREENSHOT_TRAFFIC  seconds of traffic before the live pictures (default 300: the
#                       charts show 5 minutes)
#   SCREENSHOT_SUBNET   subnet of the stand (default 10.213.77.0/24)
#   SCREENSHOT_NETWORK  use this existing Docker network instead of creating one
#   SCREENSHOT_TOOLS    folder for node_modules (default ../proximity-build/screenshots)
#   SCREENSHOT_OUTPUT   folder for the pictures (default docs/images)
# NOTE: A picture that must have changed because the code or the layout of its dialog
# changed may be replaced right away: there is no need to open the new picture and to
# compare it with the old one. Look at a picture only when it is not clear whether it
# had to change.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tools="${SCREENSHOT_TOOLS:-$root/../proximity-build/screenshots}"
output="${SCREENSHOT_OUTPUT:-$root/docs/images}"
image="${PROXIMITY_IMAGE:-}"
network="${SCREENSHOT_NETWORK:-}"
subnet="${SCREENSHOT_SUBNET:-10.213.77.0/24}"
container="proximity-screenshots"
peer="proximity-screenshots-peer"
created=""
user="admin"
password="Screenshots-1"
for tool in docker node npm; do
    if ! command -v "$tool" > /dev/null 2>&1; then
        echo "screenshots: $tool is required" >&2
        exit 1
    fi
done
mkdir -p "$tools"
cp -p "$root"/scripts/screenshots/*.json "$root"/scripts/screenshots/*.mjs "$tools/"
if [ ! "$tools/node_modules/.package-lock.json" -nt "$tools/package-lock.json" ]; then
    (cd "$tools" && npm ci --no-audit --no-fund)
fi
browser="$(node "$tools/screenshots.mjs" --browser)"
if [ -z "$image" ]; then
    image="proximity-screenshots"
    docker build -t "$image" "$root"
fi
cleanup() {
    docker rm -f -v "$container" "$peer" > /dev/null 2>&1 || true
    if [ -n "$created" ]; then
        docker network rm "$created" > /dev/null 2>&1 || true
    fi
}
cleanup
trap cleanup EXIT
if [ -z "$network" ]; then
    network="proximity-screenshots"
    docker network rm "$network" > /dev/null 2>&1 || true
    docker network create --subnet "$subnet" "$network" > /dev/null
    created="$network"
fi
aliases=()
while IFS= read -r name; do
    aliases+=(--network-alias "$name")
done < <(node "$tools/screenshots.mjs" --hosts)
docker run -d --name "$peer" --network "$network" "${aliases[@]}" --cap-add NET_ADMIN \
    --device /dev/net/tun -p 127.0.0.1::8899 -e PROXIMITY_ADMIN_USER="$user" \
    -e PROXIMITY_ADMIN_PASSWORD="$password" "$image" > /dev/null
docker run -d --name "$container" --network "$network" --cap-add NET_ADMIN \
    --device /dev/net/tun -p 127.0.0.1::8899 -p 127.0.0.1::7899 -p 127.0.0.1::7801 \
    "$image" > /dev/null
# The far side answers on the usual ports of the proxies and the VPN and plays the address
# 1.1.1.1:443, which the connectivity checks use, so the stand needs no Internet.
address="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$peer")"
docker exec "$peer" ip addr add 1.1.1.1/32 dev lo
docker exec "$peer" iptables -t nat -A PREROUTING -p tcp --dport 1080 -j REDIRECT --to-ports 7801
docker exec "$peer" iptables -t nat -A PREROUTING -p tcp --dport 3128 -j REDIRECT --to-ports 7899
docker exec "$peer" iptables -t nat -A PREROUTING -p udp --dport 1194 -j REDIRECT --to-ports 7800
docker exec "$peer" iptables -t nat -A PREROUTING -p tcp -d 1.1.1.1 --dport 443 -j REDIRECT \
    --to-ports 8899
docker exec "$peer" iptables -t nat -A OUTPUT -p tcp -d 1.1.1.1 --dport 443 -j REDIRECT \
    --to-ports 8899
docker exec "$container" ip route add 1.1.1.1/32 via "$address"
published() {
    docker port "$1" "$2" | head -n 1
}
echo "screenshots: image is '$image', browser is '$browser', the UI is at" \
    "http://$(published "$container" 8899/tcp), pictures go to '$output'"
CHROME="$browser" SCREENSHOT_USER="$user" SCREENSHOT_PASSWORD="$password" \
    SCREENSHOT_PEER="http://$(published "$peer" 8899/tcp)" \
    SCREENSHOT_PROXY="$(published "$container" 7899/tcp)" \
    SCREENSHOT_SOCKS="$(published "$container" 7801/tcp)" \
    node "$tools/screenshots.mjs" "http://$(published "$container" 8899/tcp)" "$output" "$@"
