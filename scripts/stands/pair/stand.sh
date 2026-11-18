#!/usr/bin/env bash
# Stand "pair": two linked Proximity instances in one Docker network.
#   proximity-stand-a  the client side: its outbounds connect to the other instance
#   proximity-stand-b  the server side: OpenVPN and SOCKS5 inbounds, its proxy port 7899 is
#                      an HTTP proxy, its web UI on port 8899 is a site to download from;
#                      it also answers for 1.1.1.1:443, the target of connectivity checks
# Usage: bash scripts/stands/pair/stand.sh up | down | status
# "up" prints the UI addresses and the administrator account; both instances start empty,
# a check script adds what it needs through the API (docs/api.md).
source "$(dirname "$0")/../lib.sh"
first="proximity-stand-a"
second="proximity-stand-b"
case "${1:-}" in
up)
    stand_require docker
    image="$(stand_image)"
    stand_down "$first" "$second"
    stand_network_up
    stand_instance "$second" "$image"
    stand_instance "$first" "$image"
    docker exec "$second" ip addr add 1.1.1.1/32 dev lo
    for chain in PREROUTING OUTPUT; do
        docker exec "$second" iptables -t nat -A "$chain" -p tcp -d 1.1.1.1 --dport 443 \
            -j REDIRECT --to-ports 8899
    done
    docker exec "$first" ip route add 1.1.1.1/32 via "$(stand_address "$second")"
    bash "$0" status
    ;;
down)
    stand_down "$first" "$second"
    ;;
status)
    echo "stand: a (client side) $(stand_url "$first"), address $(stand_address "$first")"
    echo "stand: b (server side) $(stand_url "$second"), address $(stand_address "$second")"
    echo "stand: user '$stand_user', password '$stand_password'"
    ;;
*)
    echo "usage: $0 up | down | status" >&2
    exit 1
    ;;
esac
