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

Results of the run on 2026-11-17 (bridge mode, one host) are in the project
document `proximity-openvpn-test-report.md`.

- [x] **Test 1: external client → Proximity OpenVPN inbound**
  - Done: 27 of 27 cases pass (16 password / TOTP, 11 certificate / key);
    HTTP, UDP, DNS and ping pass; the exit follows the selected outbound.
  - Bring up an OpenVPN server on Proximity (OpenVPN inbound).
  - Build a client config (`.ovpn` with embedded certs and keys).
  - Connect a real external OpenVPN client to the server.
  - Check that client traffic exits via the outbound selected for the inbound.

- [x] **Test 2: Proximity OpenVPN outbound → external server**
  - Done: append, static-challenge and dynamic-challenge TOTP, UDP and TCP,
    encrypted client key all connect and pass data; 9 of 9 negative cases
    end as `failed`.
  - Bring up an external OpenVPN server (separate container or host)
    with certificates and TOTP.
  - Create an OpenVPN outbound (client) in Proximity pointing to it.
  - Route traffic through this outbound and check it reaches
    the network behind the external server.

- [x] **Test 3: loopback — Proximity connected to itself**
  - Done: inside one instance the handshake (certificate + TOTP) succeeds,
    but data cannot pass: the kernel drops packets whose source address is
    local; such an outbound now fails with a clear message. Two linked
    instances (outbound of one → inbound of the other) work.
  - In one Proximity instance create both an OpenVPN inbound (server)
    and an OpenVPN outbound (client) that connects to that inbound.
  - Route test traffic into the outbound (e.g. via a SOCKS5 inbound
    or a route rule).
  - Probe the chain with external tools (`curl`, `ping`, `dig`, `tcpdump`)
    and check that traffic enters the client, passes the server
    and leaves via the server-side outbound.
  - Watch for routing loops: the outbound must not route
    its own tunnel packets back into itself.

### Open issues found by the tests

- [ ] **TOTP code can be reused**
  - The same code is accepted again within its ±30 s window, also for
    another user of the inbound (the key is shared).
  - Strict single use would reject a quick reconnect of a Proximity
    outbound in the same window; consider binding a used code to
    user + client address.

- [ ] **No certificate revocation**
  - After *Regenerate* of the client certificate the old one is still
    accepted, as is any certificate signed by the CA; only a new CA revokes.
  - Options: CRL (`crl-verify`) or pinning the current client certificate
    (`peer-fingerprint`).

- [ ] **ICMP bypasses outbounds**
  - `ping` to a destination routed to a tunnel or proxy leaves through
    the default route in clear text and gets no reply.

- [ ] **No self-recovery of a `failed` service**
  - An outbound that used all attempts stays `failed` until a manual
    Restart (server down for about 3 minutes with defaults).
  - The same after an NTP outage at start: OpenVPN services fail and
    do not start when time becomes available.

- [ ] **Default inbound networks collide between instances**
  - Every instance uses `10.77.<N>.0/24`, so two default instances cannot
    be linked (now reported as an overlap error). Consider a random
    default network.

- [ ] **Fragmentation with `tun-mtu 1500`**
  - UDP datagrams above about 1430 bytes are fragmented on the wire;
    consider a lower default MTU.

- [ ] **Killed session is not announced to the client**
  - After a user is disabled the server drops the session at once, the
    client notices only after 60 s (`ping-restart`); consider
    `client-kill <CID> HALT`.

- [ ] **Auth token does not survive an inbound restart**
  - The token key is random per server start, so every client must enter
    a new one-time code after a restart.

- [ ] **Slow or misleading connection errors through the router**
  - TCP `connect` always succeeds at once; a closed port is reported
    after about 2.1 s, an unreachable host after 5 s.
  - `connection timed out` hides `Connection refused` of a TCP server.

- [ ] **Inbound log holds 300 lines**
  - Failed handshakes push real events out quickly.

- [ ] **Not covered yet**
  - Repeat the three tests in host mode (transparent capture).
  - Put the lab (helper containers, client driver, case matrices) into
    the repository as an automated test.

## Performance

- [ ] **Investigate high latency when routing traffic through Proximity**
  - Measure baseline vs. Proximity: same request directly from the host
    and through each inbound / route / outbound (`curl -w` timings,
    `ping`, `mtr`, `dig`).
  - Split the latency by stage: DNS resolution (fake-IP, DNS hijack,
    upstream resolver), TCP/TLS connect, first byte, transfer.
  - Check the usual suspects:
    - DNS: slow or unreachable upstream, retries, timeouts, IPv6 (AAAA)
      lookups that time out, resolving via a distant outbound;
    - rule matching: GEOIP/GEOSITE lookups, rules that force DNS resolution,
      large rule sets;
    - outbound health checks, fallback / url-test switching, unreachable
      routes cache;
    - Docker networking: bridge vs. host mode, NAT, MTU / fragmentation
      (especially inside tunnels), TUN stack (gVisor vs. system);
    - CPU or memory pressure in the container, log verbosity.
  - Use mihomo tools: connection list and per-connection timings in the
    API, debug logs, `pprof` if available.
  - Capture traffic with `tcpdump` on the container interfaces to see
    where packets wait.
  - Write down findings and fixes; add a repeatable latency benchmark.
