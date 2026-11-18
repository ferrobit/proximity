#!/usr/bin/env bash
# Runs the security checks of the OpenVPN inbound on the stand "pair":
# single use of one-time codes, rejection of an old client certificate, ping of clients.
# Usage: make stand-security, or bash scripts/stands/pair/security.sh   (about 1 minute)
source "$(dirname "$0")/../lib.sh"
stand_require docker node
bash "$(dirname "$0")/stand.sh" up
if [ "${STAND_KEEP:-}" != 1 ]; then
    trap 'bash "$(dirname "$0")/stand.sh" down' EXIT
fi
node "$(dirname "$0")/security.mjs"
