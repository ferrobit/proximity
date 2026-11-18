# TODO

## DNS

- [ ] **DNS routing per outbound**
  - Classify every outbound by DNS capability:
    - can carry UDP DNS (UDP-capable tunnels: WireGuard, OpenVPN, SOCKS5 with UDP ASSOCIATE, etc.);
    - TCP only (HTTP proxy, SOCKS5 without UDP) — needs DoH/DoT/DNS-over-TCP;
    - no DNS at all — must use another exit.
  - Define the DNS exit point for each DNS server: which outbound
    (Host, Router or a created outbound) a given upstream resolver is reached through.
  - Bind DNS routing to outbounds: queries for domains routed via outbound X
    should be resolved through X (or its configured fallback), so the resolver
    location matches the traffic exit (no DNS leaks, correct geo answers).
  - Check mihomo mechanisms and pick one:
    - `nameserver-policy` (per-domain/ruleset resolvers);
    - `#<proxy>` suffix on a nameserver URL (send the DNS query via a proxy);
    - `respect-rules` + `proxy-server-nameserver`;
    - `direct-nameserver`.
  - Decide how it fits with the existing fake-IP / DNS-hijack logic.
  - Expose it in the UI (Outbounds and/or Routes pages).

## OpenVPN

All three tests use full auth: PKI (CA, server and client certificates
and keys), `tls-crypt` / `tls-auth` static key, user name + password
with TOTP (one-time code) as the second factor.

For every test verify:

- TLS handshake and TOTP auth succeed;
- wrong TOTP code, revoked or foreign certificate are rejected;
- traffic actually flows (ping, HTTP, DNS through the tunnel);
- traffic takes the expected route (selected outbound / exit point).

Automate the tests if possible (script or Go test with helper containers).

- [ ] **Test 1: external client → Proximity OpenVPN inbound**
  - Bring up an OpenVPN server on Proximity (OpenVPN inbound).
  - Build a client config (`.ovpn` with embedded certs and keys).
  - Connect a real external OpenVPN client to the server.
  - Check that client traffic exits via the outbound selected for the inbound.

- [ ] **Test 2: Proximity OpenVPN outbound → external server**
  - Bring up an external OpenVPN server (separate container or host)
    with certificates and TOTP.
  - Create an OpenVPN outbound (client) in Proximity pointing to it.
  - Route traffic through this outbound and check it reaches
    the network behind the external server.

- [ ] **Test 3: loopback — Proximity connected to itself**
  - In one Proximity instance create both an OpenVPN inbound (server)
    and an OpenVPN outbound (client) that connects to that inbound.
  - Route test traffic into the outbound (e.g. via a SOCKS5 inbound
    or a route rule).
  - Probe the chain with external tools (`curl`, `ping`, `dig`, `tcpdump`)
    and check that traffic enters the client, passes the server
    and leaves via the server-side outbound.
  - Watch for routing loops: the outbound must not route
    its own tunnel packets back into itself.
