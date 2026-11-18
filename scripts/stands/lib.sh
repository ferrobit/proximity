#!/usr/bin/env bash
# Common parts of the test stands; a stand script sources this file.
# A stand is a set of throwaway containers in its own Docker network. It does not use the
# host network and does not touch a running Proximity or its settings.
# Variables:
#   PROXIMITY_IMAGE  use this ready image instead of building "proximity-stand" from the
#                    working tree
#   STAND_SUBNET     subnet of the stand network (default 10.213.78.0/24)
#   STAND_OFFLINE    1: the stand must work without Internet; a local time server answers
#                    for the default NTP servers (OpenVPN starts only with checked time)
#   STAND_KEEP       1: a check script leaves the stand running at the end
set -euo pipefail
stand_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
stand_network="proximity-stand"
stand_subnet="${STAND_SUBNET:-10.213.78.0/24}"
stand_user="admin"
stand_password="Stand-admin-1"
stand_time="proximity-stand-time"

stand_require() {
    for tool in "$@"; do
        if ! command -v "$tool" > /dev/null 2>&1; then
            echo "stand: $tool is required" >&2
            exit 1
        fi
    done
}

stand_image() {
    if [ -n "${PROXIMITY_IMAGE:-}" ]; then
        echo "$PROXIMITY_IMAGE"
        return
    fi
    docker build -q -t proximity-stand "$stand_root" > /dev/null
    echo proximity-stand
}

stand_network_up() {
    docker network inspect "$stand_network" > /dev/null 2>&1 ||
        docker network create --subnet "$stand_subnet" "$stand_network" > /dev/null
    if [ "${STAND_OFFLINE:-}" = 1 ] && ! docker inspect "$stand_time" > /dev/null 2>&1; then
        docker run -d --name "$stand_time" --network "$stand_network" \
            --network-alias time.cloudflare.com --network-alias time.google.com \
            --network-alias pool.ntp.org -v "$stand_root/scripts/stands/time:/time:ro" \
            "${STAND_TIME_IMAGE:-python:3-alpine}" python3 /time/server.py > /dev/null
    fi
}

# stand_instance <container> [docker run options ...]: a Proximity instance with the
# administrator of the stand, a tunnel device and the UI on a free port of 127.0.0.1.
stand_instance() {
    local name="$1"
    shift
    docker run -d --name "$name" --network "$stand_network" --cap-add NET_ADMIN \
        --device /dev/net/tun -p 127.0.0.1::8899 -e PROXIMITY_ADMIN_USER="$stand_user" \
        -e PROXIMITY_ADMIN_PASSWORD="$stand_password" "$@" > /dev/null
}

stand_url() {
    echo "http://$(docker port "$1" 8899/tcp | head -n 1)"
}

stand_address() {
    docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"
}

stand_down() {
    docker rm -f -v "$@" > /dev/null 2>&1 || true
    if [ -z "$(docker ps -a --format '{{.Names}}' | grep '^proximity-stand-' |
        grep -v "^$stand_time\$" || true)" ]; then
        docker rm -f -v "$stand_time" > /dev/null 2>&1 || true
        docker network rm "$stand_network" > /dev/null 2>&1 || true
    fi
}
