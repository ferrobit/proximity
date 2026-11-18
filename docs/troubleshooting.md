# Troubleshooting

Start with four places in the UI — they answer most questions:

1. **Dashboard → Connection map**: red items are down or failed, gray ones are disabled or
   stopped. Hover over a route to see which outbound it really reaches (through a fallback,
   too) and whether traffic flows there.
2. **Outbounds → 📄 Connection log** of the outbound in question.
3. **Dashboard → Unreachable routes**: destination, reason, the outbound used and the rule
   that matched.
4. **Routes → Router configuration**: the rules exactly as mihomo received them.

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
| `authentication failed` (OpenVPN) | Check user name and password. Without an OTP key Proximity does not retry, to avoid locking the account; with a key it uses the *Connection attempts* once. *Restore lost connections* never repeats a rejected login |
| `OTP key is required by server` | Fill in *OTP key* (base32 secret) and choose *OTP code usage*: answer the `CHALLENGE` prompt or append the code to the password |
| `… is required by server` | The server asked for something not configured (user name, password, private key password). The outbound fails at once and is not retried |
| `invalid private key password` (OpenVPN) | The client key in the configuration is encrypted and *Private key password* does not open it |
| `server certificate is rejected: …` (OpenVPN) | The certificate of the server does not pass the check against `<ca>` of the configuration (wrong or old CA, expired certificate, another server name); the text is the reason printed by OpenVPN. Not retried |
| `tunnel address '…' overlaps network '…' of inbound device 'proximity_i<N>'` (OpenVPN) | The VPN server gave an address from the *VPN network* of one of your OpenVPN inbounds, so the kernel drops the tunnel packets. Typical for two Proximity instances connected to each other when their inbounds still have the old default `10.77.<N>.0/24` (a new inbound gets `10.<X>.<N>.0/24` with its own `<X>` per installation). Change *VPN network* of the inbound on one side. An outbound connected to an inbound of the same instance always fails this way |
| `device 'proximity_a<N>' is not ready` (AmneziaWG) | `amneziawg-go` did not create its interface and control socket within 10 s; see the log for its own error |
| `connection timed out` (OpenVPN) | The server is not reachable or the configuration is wrong; OpenVPN did not finish within 60 s |
| `monitoring failed after N attempts: failed to ping gateway …` | The tunnel is up but the gateway does not answer ping. Some servers block ICMP — disable *Enable monitoring* for this outbound |
| `failed to run 'awg setconf …'` (AmneziaWG) | Check the configuration: keys and the Amnezia parameters (`Jc`, `S1`, `H1`…) must match the server |
| HOST outbound `failed` | *Probe target* (Settings, default `1.1.1.1:443`) is not reachable through this uplink. Change it to a host your network allows |

With *Restore lost connections* (the **Monitoring** tab, on by default) a tunnel that
lost its server stays `reconnecting` with the message `recovery attempt N: …` and comes
back by itself; a `failed` HOST outbound keeps being checked and returns to `good`. A
tunnel is `failed` when recovery is off, when *Recovery attempts* are used up (the log
says `recovery is stopped after N attempts`) or when the error needs your action. It then
waits for **Start**, **Restart** or a configuration change — also after a container
restart; its log keeps the lines of the failed attempt.

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

## Ping of the host does not answer (Linux)

With capture on, ping cannot pass a proxy. **Settings → Keep ping of the host on its
routes** (on by default) lets a ping out directly only to addresses whose route or the
default target is **DIRECT** or a **HOST** outbound, sends it through the tunnel for an
OpenVPN or AmneziaWG outbound (the tunnel must be up), and other addresses (proxies,
fallbacks, REJECT) do not answer. Check such an address with TCP (`curl`) instead, or
switch the option off: then every ping leaves directly, past the routes. A route that
cannot be checked by address (GEOIP, IP-ASN, a source or a process) with a target that is
not direct stops ping to every address that no earlier route allows.

```bash
sudo iptables -S PROXIMITY-PING           # RETURN: ping may go out directly, DROP: it may not
ip rule show | grep -E '^516[56]'         # ping into tunnels: lookup <table of the outbound>
sudo iptables -t nat -S POSTROUTING | grep 198.18   # source of ping into tunnels
```

## No internet on the host after a crash

When the container stops normally, Proximity removes its rules. If the process was killed,
the TUN device disappears and traffic returns to the normal path automatically. If you
still need to clean up by hand:

```bash
for p in 5150 5155 5160 5170; do while sudo ip rule del priority $p 2>/dev/null; do :; done; done
sudo ip route flush table 51000
```

## An inbound is `failed`

Hover the state or open the inbound log (📄).

| Message | What to do |
|---|---|
| `outbound '…' is disabled` / `does not exist` | Enable the outbound or select another one in the inbound. Proximity does not fall back to another exit on its own |
| `anonymous connections are not allowed and enabled users do not exist` | Add an enabled user or allow anonymous connections |
| `router does not listen on port (…)` | SOCKS5: the port is taken by another program on the host (host mode), or the router is not running — see the header badge |
| `Options error: …` | OpenVPN rejected the configuration; the text names the directive |
| `failed to enable IP forwarding` | Host mode: run `sudo sysctl -w net.ipv4.ip_forward=1` (Docker normally sets it) |

## An OpenVPN client connects but nothing opens

1. The inbound log must contain `client traffic is attached to router device
   'proximity_tun'`. If not, the router is not running.
2. Host mode: forwarded packets must pass the host firewall. Proximity inserts
   `FORWARD -i/-o proximity_i<N> -j ACCEPT` with the tool from `PROXIMITY_IPTABLES`; the
   log line `forwarding rules are not installed` shows a failure. Hosts with the legacy
   backend need `PROXIMITY_IPTABLES=iptables-legacy`; firewalld and ufw may need their own
   rule that allows forwarding from `proximity_i+`.
3. The client must reach the *Public address* and port (UDP by default) from outside:
   check port forwarding on your router for 7800–7898.
4. IPv6 is not tunnelled: a dual-stack client keeps using its own IPv6 route.
5. `ping` from a client to another host answers *Packet filtered*: the router carries
   only TCP and UDP, a ping could not follow the routes, so the server rejects it. Check
   with TCP (`curl`) instead; `ping` of the server's tunnel address works, and so does
   any `ping` when the inbound's *Outbound* is **Host**.

## An OpenVPN client gets `AUTH_FAILED`

The inbound log shows the reason printed by the check: `unexpected user`, `invalid
password`, `invalid one-time code` (the clocks of the server and the phone must agree
within 30 seconds), `anonymous connections are not allowed`. Export the configuration
again after regenerating keys or the TOTP key.

`one-time code of user '…' is already used`: a code works once; another user or another
address must use the next code (wait up to 30 seconds). A Proximity outbound waits by
itself: its log says `waiting … for a new one-time code`.

## An OpenVPN client stopped connecting after *Regenerate*

The inbound accepts only its current client certificate. The inbound log shows
`client certificate is rejected: it is not the current client certificate of this
inbound`; the client waits and reports a TLS handshake timeout. Export the client
configuration again and give it to the client.

## "Proximity started with a damaged configuration"

`proximity.json` had blocks that could not be read or checked (edited by hand, damaged
disk, a file of another version). Proximity dropped only those blocks and started with
the rest; the message and the container log (`configuration problem: …`) list them with
the reasons. The file as it was is kept beside it as `proximity.invalid-<time>.json`:
take the lost blocks from there, fix them and add them again in the UI (a route or an
inbound that pointed to a dropped outbound works again when the outbound is back under
the same ID). A file that is not JSON at all gives an empty configuration and the
first-run dialog. Close the message as Administrator when you are done.

## Inbound traffic counters stay at zero

SOCKS5 counters need `iptables` inside the container (included in the image) and the
`NET_ADMIN` capability. Counters include IP and TCP headers. OpenVPN counters are read from
the server device and need no extra tools.

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
- **Routes → Router configuration** saved with *Hide passwords* on.
