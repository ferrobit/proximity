# Network modes

Proximity runs in one of two network modes. `scripts/proximity.sh` (and `proximity.ps1`)
choose the right one for your system.

| Mode | Compose file | Behaviour |
|---|---|---|
| **Linux, transparent** (default) | `docker-compose.yml` | The container shares the host network. **All new outgoing connections of the host** go through Proximity; programs need no proxy settings |
| **macOS, Windows, or Linux without capture** | `docker-compose.bridge.yml` | The container gets its own network `proximity_lan`; programs use the proxy `127.0.0.1:7899` (HTTP and SOCKS5 on one port) |

To run Linux without capture:

```bash
PROXIMITY_NETWORK=bridge scripts/proximity.sh up -d --build
```

The choice is remembered in `proximity.env` of the settings folder and reused by the next
`up`. All variables are listed in [Getting started](getting-started.md#environment-variables).

## Transparent capture on Linux

- mihomo creates the `proximity_tun` interface. Proximity adds `ip rule` entries with
  priorities 5150–5170 and routing table 51000 that points into this interface.
- Only **new outgoing** connections are captured. Local networks, Docker networks and
  inbound connections (SSH, the Proximity UI itself) keep working as before.
- The domain used by DOMAIN routes comes from DNS (fake-IP) and from the connection
  itself (TLS SNI, HTTP `Host` header, QUIC).
- Host DNS queries are intercepted, and the name is resolved by the rule's target:
  remotely for SOCKS5/HTTP proxies, through the tunnel's DNS for VPNs (when enabled),
  through the DNS servers written on the **DNS** tab of the outbound, through the default
  DNS servers for DIRECT. Names that exist only inside a VPN or behind a proxy therefore
  work without any proxy settings in programs. Details: [DNS](dns.md).
- Traffic of mihomo itself and of tunnel transports is marked with `fwmark 51000` and is
  never captured (no loops).
- When the container stops, the rules are removed. If the process is killed, the TUN
  interface disappears together with its route, and traffic returns to the normal path.
- Turn it off in **Settings → [Router] → Route all outgoing connections of the host
  through Proximity**.
- IPv6 is captured only when **IPv6** is enabled in Settings.
- Ping cannot pass a proxy. **Settings → Keep ping of the host on its routes** (on by
  default) lets it out directly only where the route is DIRECT or HOST, sends it through
  the tunnel for OpenVPN and AmneziaWG routes; other addresses do not answer, so a ping
  never leaves past the routes. **Tools → Ping** shows which way a ping of a client takes
  (see the [user guide](user-guide.md#ping)).

The internals (rule priorities, marks, the DNS hijack, `rp_filter`) are in
[Architecture](architecture.md#transparent-capture). Problems:
[Transparent mode is not active](troubleshooting.md#transparent-mode-is-not-active-linux).
