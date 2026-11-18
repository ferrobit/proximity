#!/usr/bin/env bash
# Stand "capture": transparent capture of a Linux host (Proximity in host network mode).
#   proximity-stand-host     the "host": a network namespace with its default route via
#                            the wan container and strict reverse path filter (rp_filter=1);
#                            the checks run their commands here
#   proximity-stand-capture  Proximity in the network of the host (PROXIMITY_NETWORK=host),
#                            capture on, a SOCKS5 and an OpenVPN outbound to the exit, the
#                            routes IP-CIDR 1.1.1.1/32 -> SOCKS5 and 1.1.1.3/32 -> OpenVPN,
#                            an AmneziaWG outbound with "Receive routes"
#   proximity-stand-exit     Proximity with an anonymous SOCKS5 inbound (port 7800) and an
#                            OpenVPN inbound (port 7801, user alice, exit through the host)
#   proximity-stand-awg      an AmneziaWG server (amneziawg-go, port 51820/udp, obfuscation
#                            Jc/S1/S2/H1-H4) that masquerades its clients; its peer config
#                            has AllowedIPs = 1.1.1.4/32, 10.66.66.0/24
#   proximity-stand-wan      the "Internet": owns 1.1.1.1, 1.1.1.2 (no route: DIRECT),
#                            1.1.1.3 and 1.1.1.4, serves HTTP on port 80 and counts the
#                            packets that come to it by source and destination (iptables)
# Checks where TCP and ICMP of the host leave: through the route or past it, with the setting
# "Keep ping of the host on its routes" (ping_filter) on (ping of a tunnel route goes through
# the tunnel), with other default targets and off. A local time server stands for NTP.
# Usage: make stand-capture, or bash scripts/stands/capture/check.sh   (about 1 minute)
# Variables: those of ../lib.sh.
source "$(dirname "$0")/../lib.sh"
stand_require docker node
host="proximity-stand-host"
capture="proximity-stand-capture"
exit_name="proximity-stand-exit"
wan="proximity-stand-wan"
awg="proximity-stand-awg"
# NOTE: Runs in the AmneziaWG server; writes the config of its only peer to /tmp/client.conf.
awg_setup='set -e
server="$(awg genkey)"
client="$(awg genkey)"
params="Jc = 3\nJmin = 10\nJmax = 50\nS1 = 15\nS2 = 18\nH1 = 1234567\nH2 = 2345678\nH3 = 3456789\nH4 = 4567890"
printf "[Interface]\nPrivateKey = %s\nListenPort = 51820\n$params\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.66.66.2/32\n" \
    "$server" "$(echo "$client" | awg pubkey)" > /tmp/server.conf
for attempt in $(seq 50); do [ -S /var/run/amneziawg/awg0.sock ] && break; sleep 0.2; done
awg setconf awg0 /tmp/server.conf
ip address add 10.66.66.1/24 dev awg0
ip link set awg0 up
iptables -t nat -A POSTROUTING -s 10.66.66.0/24 -o eth0 -j MASQUERADE
printf "[Interface]\nPrivateKey = %s\nAddress = 10.66.66.2/32\n$params\n[Peer]\nPublicKey = %s\nEndpoint = %s:51820\nAllowedIPs = 1.1.1.4/32, 10.66.66.0/24\nPersistentKeepalive = 25\n" \
    "$client" "$(echo "$server" | awg pubkey)" "$ENDPOINT" > /tmp/client.conf'
export STAND_OFFLINE="${STAND_OFFLINE:-1}"
image="$(stand_image)"
stand_down "$capture" "$host" "$exit_name" "$awg" "$wan"
stand_network_up
if [ "${STAND_KEEP:-}" != 1 ]; then
    trap 'stand_down "$capture" "$host" "$exit_name" "$awg" "$wan"' EXIT
fi
docker run -d --name "$wan" --network "$stand_network" --cap-add NET_ADMIN \
    -e PROXIMITY_LISTEN=0.0.0.0:80 "$image" > /dev/null
docker exec "$wan" ip addr add 1.1.1.1/32 dev lo
docker exec "$wan" ip addr add 1.1.1.2/32 dev lo
docker exec "$wan" ip addr add 1.1.1.3/32 dev lo
docker exec "$wan" ip addr add 1.1.1.4/32 dev lo
docker exec "$wan" iptables -N STAND
docker exec "$wan" iptables -I INPUT -j STAND
stand_instance "$exit_name" "$image"
docker exec "$exit_name" ip route add 1.1.0.0/16 via "$(stand_address "$wan")"
docker run -d --name "$awg" --network "$stand_network" --cap-add NET_ADMIN \
    --device /dev/net/tun --sysctl net.ipv4.ip_forward=1 --entrypoint sleep "$image" \
    infinity > /dev/null
docker exec "$awg" ip route add 1.1.0.0/16 via "$(stand_address "$wan")"
docker exec -d "$awg" amneziawg-go --foreground awg0
docker exec -e ENDPOINT="$(stand_address "$awg")" "$awg" bash -c "$awg_setup"
docker run -d --name "$host" --network "$stand_network" --cap-add NET_ADMIN \
    --sysctl net.ipv4.conf.all.rp_filter=1 --sysctl net.ipv4.conf.default.rp_filter=1 \
    -p 127.0.0.1::8899 --entrypoint sleep "$image" infinity > /dev/null
docker exec "$host" ip route replace default via "$(stand_address "$wan")"
docker run -d --name "$capture" --network "container:$host" --cap-add NET_ADMIN \
    --device /dev/net/tun -e PROXIMITY_NETWORK=host -e PROXIMITY_ADMIN_USER="$stand_user" \
    -e PROXIMITY_ADMIN_PASSWORD="$stand_password" "$image" > /dev/null
# NOTE: check.mjs reads the counters in this order.
for source in "$(stand_address "$host")" "$(stand_address "$exit_name")" \
    "$(stand_address "$awg")"; do
    for target in 1.1.1.1 1.1.1.2 1.1.1.3 1.1.1.4; do
        docker exec "$wan" iptables -A STAND -s "$source" -d "$target" -p icmp \
            --icmp-type echo-request
        docker exec "$wan" iptables -A STAND -s "$source" -d "$target" -p tcp --dport 80 --syn
    done
done
node "$(dirname "$0")/check.mjs" "$host" "$exit_name" "$wan" "$awg"
