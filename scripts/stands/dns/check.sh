#!/usr/bin/env bash
# Stand "dns": which DNS server gets a query and where the query leaves, with transparent
# capture of a Linux host (fake-IP for the applications, strict reverse path filter).
#   proximity-stand-host     the "host": a network namespace with its default route via the
#                            wan container, rp_filter=1 and "nameserver 1.1.1.1" in
#                            resolv.conf; the checks run their commands here
#   proximity-stand-capture  Proximity in the network of the host (PROXIMITY_NETWORK=host),
#                            capture on; its outbounds name their servers (exit.stand.test,
#                            vpn.stand.test) and have their own DNS servers and exits
#   proximity-stand-exit     Proximity with two SOCKS5 inbounds (7800 with UDP, 7802 without)
#                            and an OpenVPN inbound (7801, user alice), exit through the host
#   proximity-stand-wan      the "Internet": owns 1.1.1.1 (the DNS server of the system),
#                            1.1.1.53 (the DNS server of the outbounds) and 1.1.1.2 (HTTP)
#   proximity-stand-dns      the DNS server of both addresses (server.py in the network of the
#                            wan container): prints every query with the address it came from
# Checks: the names of the configuration (a proxy server, a VPN remote, the time servers) are
# resolved for real and never through their own outbound, an application gets a placeholder
# address, the domains of a route are asked from the DNS servers of its outbound through the
# outbound (UDP, TCP for a proxy without UDP, the far side of a tunnel, another exit), the
# default servers of Settings, and the answers pass rp_filter=1.
# Usage: make stand-dns, or bash scripts/stands/dns/check.sh   (about 1 minute)
# Variables: those of ../lib.sh.
source "$(dirname "$0")/../lib.sh"
stand_require docker node
host="proximity-stand-host"
capture="proximity-stand-capture"
exit_name="proximity-stand-exit"
wan="proximity-stand-wan"
dns="proximity-stand-dns"
export STAND_OFFLINE="${STAND_OFFLINE:-1}"
image="$(stand_image)"
stand_down "$capture" "$host" "$exit_name" "$dns" "$wan"
stand_network_up
if [ "${STAND_KEEP:-}" != 1 ]; then
    trap 'stand_down "$capture" "$host" "$exit_name" "$dns" "$wan"' EXIT
fi
docker run -d --name "$wan" --network "$stand_network" --cap-add NET_ADMIN \
    -e PROXIMITY_LISTEN=0.0.0.0:80 "$image" > /dev/null
for address in 1.1.1.1 1.1.1.2 1.1.1.53; do
    docker exec "$wan" ip addr add "$address/32" dev lo
done
stand_instance "$exit_name" "$image"
docker exec "$exit_name" ip route add 1.1.0.0/16 via "$(stand_address "$wan")"
docker exec "$exit_name" sh -c 'echo "nameserver 1.1.1.1" > /etc/resolv.conf'
names="exit.stand.test=$(stand_address "$exit_name") vpn.stand.test=$(stand_address "$exit_name")"
for name in time.cloudflare.com time.google.com pool.ntp.org; do
    names="$names $name=$(stand_address "$stand_time")"
done
# shellcheck disable=SC2086
docker run -d --name "$dns" --network "container:$wan" \
    -v "$stand_root/scripts/stands/dns:/dns:ro" "${STAND_TIME_IMAGE:-python:3-alpine}" \
    python3 /dns/server.py 1.1.1.1,1.1.1.53 $names > /dev/null
docker run -d --name "$host" --network "$stand_network" --cap-add NET_ADMIN \
    --sysctl net.ipv4.conf.all.rp_filter=1 --sysctl net.ipv4.conf.default.rp_filter=1 \
    -p 127.0.0.1::8899 --entrypoint sleep "$image" infinity > /dev/null
docker exec "$host" ip route replace default via "$(stand_address "$wan")"
docker exec "$host" sh -c 'echo "nameserver 1.1.1.1" > /etc/resolv.conf'
docker run -d --name "$capture" --network "container:$host" --cap-add NET_ADMIN \
    --device /dev/net/tun -e PROXIMITY_NETWORK=host -e PROXIMITY_ADMIN_USER="$stand_user" \
    -e PROXIMITY_ADMIN_PASSWORD="$stand_password" "$image" > /dev/null
node "$(dirname "$0")/check.mjs" "$host" "$exit_name" "$dns"
