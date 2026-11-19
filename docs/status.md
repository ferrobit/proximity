# Project status

Version **1.2.0**. 167 Go tests pass, total statement coverage 94.2% (81.4–99.1% per
package). The numbers are written by `make coverage-docs`; per-package coverage is in
[Development](development.md#tests).

## Verified

- **Transparent capture** in isolated network namespaces and in a real Docker image:
  DIRECT, REJECT, domain → OpenVPN / AmneziaWG, LAN and inbound connections untouched,
  `rp_filter=1`, container stop and kill.
- **Inbounds** with real mihomo 1.19.34 and OpenVPN 2.6 in network namespaces: SOCKS5
  authentication and TLS, an OpenVPN client with password + TOTP (appended and
  `static-challenge`), anonymous clients, token renegotiation, session kill on user change,
  traffic through the router, DNS hijack, traffic counters.
- **Ready stands** with the real image ([Development](development.md#test-stands)):
  one-time codes, certificates, tokens, received routes, a damaged configuration, the
  reload of an open page, TCP and ping of a host with transparent capture, an AmneziaWG
  tunnel with obfuscation and received routes, the DNS servers and DNS exits of outbounds
  (UDP, TCP, DNS over TLS and HTTPS through SOCKS5, an HTTP proxy and a tunnel, names of
  servers, keyword and regex routes), clearing the logs, route hit counters, the page
  Tools (address check and ping).
- **Web UI**, including the connection map, in Chrome in the light and dark themes with a
  live OpenVPN tunnel, SOCKS5 and HTTP proxies and both inbound types.

## Not verified yet

- Inbounds inside a real Docker container (published port range, host mode with Docker's
  `FORWARD` policy, firewalld / ufw hosts), OpenVPN clients on Windows, macOS and phones.
- `docker-compose.yml` with a real `network_mode: host` on a physical machine;
  `PROCESS-NAME` routes.
- Interaction of capture with systemd-resolved, NetworkManager, firewalld and other VPN
  clients on the host.
- macOS and Windows (Docker Desktop), the `proximity.ps1` script.
- IPv6 capture, dynamic OTP challenge (CRV1), `tap` devices.
- Importing a real Amnezia `vpn://` key and QR code.

Bug reports and test results from these environments are very welcome. Known limits and
planned work are in [`TODO.md`](../TODO.md).
