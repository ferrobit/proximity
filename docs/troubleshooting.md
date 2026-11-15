# Troubleshooting

Start with three places in the UI — they answer most questions:

1. **Outbounds → 📄 Connection log** of the outbound in question.
2. **Dashboard → Unreachable routes**: destination, reason, the outbound used and the rule
   that matched.
3. **Routes → Router configuration**: the rules exactly as mihomo received them.

And the container log:

```bash
scripts/proximity.sh logs -f --tail 200
```

---

## The web UI does not open

- `scripts/proximity.sh ps` — is the container `running (healthy)`?
- Port 8899 may be taken by another program: `ss -ltnp | grep 8899`.
- In host mode the UI listens on `PROXIMITY_UI_BIND:8899` (default `0.0.0.0`); in bridge
  mode the port is published by Docker. Check that a firewall does not block it.

## I forgot the administrator password

There is no reset button by design. Stop the container, remove the users from the settings
file and start again — the **Create Administrator Account** dialog will appear:

```bash
scripts/proximity.sh down
cp ~/.config/proximity/proximity.json ~/proximity.json.bak
jq '.users = []' ~/proximity.json.bak > ~/.config/proximity/proximity.json
scripts/proximity.sh up -d
```

Alternatively set `PROXIMITY_ADMIN_USER` / `PROXIMITY_ADMIN_PASSWORD` before starting —
they are used only when there are no users.

## An outbound is `failed`

| Message in the log / status | What to do |
|---|---|
| `authentication failed` (OpenVPN) | Check user name and password. Without an OTP key Proximity does not retry, to avoid locking the account |
| `OTP key is required by server` | Fill in *OTP key* (base32 secret) and choose *OTP code usage*: answer the `CHALLENGE` prompt or append the code to the password |
| `… is required by server` | The server asked for something not configured (user name, password, private key password) |
| `device 'proximity_a<N>' is not ready` (AmneziaWG) | `amneziawg-go` did not create its interface and control socket within 10 s; see the log for its own error |
| `connection timed out` (OpenVPN) | The server is not reachable or the configuration is wrong; OpenVPN did not finish within 60 s |
| `monitoring failed after N attempts: failed to ping gateway …` | The tunnel is up but the gateway does not answer ping. Some servers block ICMP — disable *Enable monitoring* for this outbound |
| `failed to run 'awg setconf …'` (AmneziaWG) | Check the configuration: keys and the Amnezia parameters (`Jc`, `S1`, `H1`…) must match the server |
| HOST outbound `failed` | *Probe target* (Settings, default `1.1.1.1:443`) is not reachable through this uplink. Change it to a host your network allows |

A `failed` HOST outbound keeps being checked and returns to `good` by itself. A `failed`
tunnel waits for **Reconnect** or a configuration change.

## Traffic goes through the wrong outbound

1. Look at **Dashboard → Unreachable routes** or the router log: the *Matched rule* column
   shows which rule caught the connection.
2. Routes are checked **top to bottom**; a broader route above wins. The warning at the
   top of the Routes page lists rules that can never match.
3. `DOMAIN-KEYWORD` matches substrings: `ads` also matches `downloads.example.org`.
4. A disabled outbound or fallback makes its routes inactive (*Some routes are not
   active*). A disabled default target falls back to `DIRECT`.
5. In transparent mode a connection made by IP address matches DOMAIN rules only if the
   domain can be sniffed (TLS SNI, HTTP Host, QUIC). Otherwise use `IP-CIDR` rules.

## Intranet names do not resolve

- On the VPN outbound enable **Use DNS servers pushed by VPN** (OpenVPN) or **Use DNS
  servers from configuration** (AmneziaWG). The route's domains are then resolved through
  the tunnel.
- In bridge mode programs must send the *name* to the proxy, not an IP they resolved
  themselves: use `socks5h://` in curl, "proxy DNS when using SOCKS" in Firefox, or the
  HTTP proxy.

## Transparent mode is not active (Linux)

The header shows **transparent** when capture is working. If it does not:

- you are using the bridge compose file (`PROXIMITY_NETWORK=bridge`), or
- **Settings → Route all outgoing connections of the host through Proximity** is off, or
- the TUN device could not be created — check `/dev/net/tun` and `NET_ADMIN`.

On the host:

```bash
ip rule show | grep -E '^51[5-7]0'     # capture rules 5150–5170
ip route show table 51000              # default dev proximity_tun
ip link show proximity_tun
```

## No internet on the host after a crash

When the container stops normally, Proximity removes its rules. If the process was killed,
the TUN device disappears and traffic returns to the normal path automatically. If you
still need to clean up by hand:

```bash
for p in 5150 5155 5160 5170; do while sudo ip rule del priority $p 2>/dev/null; do :; done; done
sudo ip route flush table 51000
```

## "Router configuration is not applied"

mihomo rejected the generated configuration (`mihomo -t` failed). The message shows the
reason. Typical causes: an unknown GEOSITE category, a regular expression mihomo does not
accept, a missing GeoIP database file. Fix or disable the route; the previous working
configuration stays active meanwhile.

## The throughput chart is empty after a restart

By design: the chart history (1 hour, 5-second samples) is kept only in memory. Totals in
the *Traffic* column are saved every 30 seconds and survive restarts.

## Replies from a tunnel are dropped (`rp_filter`)

Proximity adds a rule by tunnel source address, so strict reverse path filtering
(`rp_filter=1`) works. The bridge compose file additionally sets `rp_filter=2` and
`src_valid_mark=1` inside the container. If you run a custom setup, apply the same sysctls.

## Reporting a problem

Please include:

- OS, Docker version, compose file used (host or bridge);
- `scripts/proximity.sh logs --tail 300`;
- the connection log of the affected outbound (passwords are not printed there, but
  check before posting);
- **Routes → Router configuration** with secrets removed.
