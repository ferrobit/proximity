#!/usr/bin/env bash
# Runs the checks of an OpenVPN link on the stand "pair": defaults of a new inbound, the
# route that follows the routes of the VPN connection, a disabled user, the token that
# outlives a restart of the inbound, the instance mark for the reload of an open page.
# Usage: make stand-tunnel, or bash scripts/stands/pair/tunnel.sh   (about 1.5 minutes)
source "$(dirname "$0")/../lib.sh"
stand_require docker node
bash "$(dirname "$0")/stand.sh" up
if [ "${STAND_KEEP:-}" != 1 ]; then
    trap 'bash "$(dirname "$0")/stand.sh" down' EXIT
fi
node "$(dirname "$0")/tunnel.mjs"
