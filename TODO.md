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
