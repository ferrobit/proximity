#!/usr/bin/env bash
# Stand "broken-config": one Proximity instance started with the damaged configuration
# scripts/stands/broken-config/config.json (an outbound that cannot be read, wrong values,
# a duplicate, a fallback, routes and an inbound that depend on them).
# Usage: make stand-config, or bash scripts/stands/broken-config/check.sh
#        starts the stand, runs check.mjs, removes the stand (STAND_KEEP=1 leaves it
#        running; the UI address is printed)
source "$(dirname "$0")/../lib.sh"
container="proximity-stand-broken"
stand_require docker node
image="$(stand_image)"
stand_down "$container"
if [ "${STAND_KEEP:-}" != 1 ]; then
    trap 'stand_down "$container"' EXIT
fi
stand_network_up
docker create --name "$container" --network "$stand_network" --cap-add NET_ADMIN \
    --device /dev/net/tun -p 127.0.0.1::8899 -e PROXIMITY_ADMIN_USER="$stand_user" \
    -e PROXIMITY_ADMIN_PASSWORD="$stand_password" "$image" > /dev/null
docker cp "$(dirname "$0")/config.json" "$container:/config/proximity.json"
docker start "$container" > /dev/null
echo "stand: broken-config $(stand_url "$container"), user '$stand_user'," \
    "password '$stand_password'"
node "$(dirname "$0")/check.mjs"
