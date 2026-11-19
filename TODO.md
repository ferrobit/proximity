# TODO

## DNS

- [x] **DNS routing per outbound**
  - Done: every outbound has the tab **DNS**: its DNS servers (plain, `tcp://`, `tls://`,
    `https://`, `quic://`) and the exit of the queries (this outbound, Host, Router, another
    outbound or fallback). The domains of the `DOMAIN`, `DOMAIN-SUFFIX` and `GEOSITE` routes
    to an outbound are asked from its servers through it (`nameserver-policy`, `server#exit`),
    in the order of the routes. Default servers and their exit: **Settings → Router → DNS**;
    names without a route follow the DNS of the default target. Configuration version 10.
  - Classes: HOST, tunnels and SOCKS5 with UDP carry everything; SOCKS5 without UDP and HTTP
    carry no UDP (a server given by address is asked over TCP, `udp://` and `quic://` are
    skipped with a warning); an outbound that cannot carry DNS at all gets another exit.
  - mihomo mechanisms (checked with 1.19.34): `nameserver-policy` + the `#<proxy>` suffix are
    used, `#RULES` for the exit Router, `default-nameserver` for the names of DNS servers.
    `respect-rules`, `proxy-server-nameserver` and `direct-nameserver` are not needed: without
    them the names of proxy servers follow the policy, where the generator pins them.
  - Names of the configuration (a proxy server, an OpenVPN `remote`, an AmneziaWG `Endpoint`,
    a check target, a time server) are resolved by Proximity through the resolver of the
    router: they follow the same DNS routing, get a real address (an application gets
    fake-IP in transparent mode, before this a tunnel with a remote name could not connect
    there) and are never asked through their own outbound (the loop is cut by a pin).
  - Found on the way: with `rp_filter=1` the answers of DNS servers to the router were
    dropped (rule 5155 had no source); fixed.
  - Checked: unit tests and the stand `dns` (`make stand-dns`, 25 checks).
  - Original task:
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


- [ ] **Names of DNS servers follow the DNS routing**
  - `https://dns.corp.example/dns-query` on the DNS tab of a VPN outbound: the name of the
    server is resolved by the default servers (`default-nameserver`) or by the resolver of
    the system, not by the DNS of the VPN. Now: write the address. An idea: resolve such a
    name through the servers of the same outbound that are given by address and put it into
    `hosts` of the router.

- [ ] **A check target reached through a tunnel is resolved by the DNS of the tunnel**
  - The check target of AmneziaWG (and the health check URL of a fallback) given by name is
    resolved by the DNS of the route that takes its domain; without such a route by the
    default servers, though the connection goes through the tunnel.

- [ ] **Placeholder answers of a Proximity OpenVPN inbound**
  - An OpenVPN inbound answers the DNS queries of its clients with fake-IP. A name of the
    configuration asked through a tunnel to another Proximity gets such an address and is
    refused (`has no usable address`). Decide: real answers for the names of the settings,
    or an option of the inbound.

- [ ] **DNS binding of keyword, wildcard and regex routes**
  - `DOMAIN-KEYWORD`, `DOMAIN-WILDCARD` and `DOMAIN-REGEX` routes do not bind their domains to
    the DNS of the outbound (a `nameserver-policy` key cannot express them): such names use
    the DNS of everything else.

- [ ] **DNS: check through a real DoH / DoT server and an HTTP proxy**
  - The stand `dns` checks plain DNS over UDP and TCP. `tls://`, `https://`, `quic://` and
    the exit through an HTTP proxy (port 443 through `ID~https`) are covered by unit tests
    and by `mihomo -t` only.

## Review 2026-11-19

Found by a review of the whole project (code, tests, build, security), not by a bug report.

- [ ] **Login: no limit on attempts**
  - A wrong password waits 700 ms, but parallel requests are not limited: a password can be
    guessed with many connections at once. Add a growing delay or a lockout per address and
    per user name (for example 5 failures → 1 minute, doubling), shown in the server log.

- [ ] **Web UI only over plain HTTP**
  - Port 8899 has no TLS (encrypted connection); the session cookie has no `Secure` flag, the
    password and the cookie cross the LAN in clear text. Add an optional TLS mode with a
    self-signed certificate (the SOCKS5 inbound already generates one) and `Secure` cookies
    in that mode.

- [ ] **Verbose logs write the file line by line**
  - `logBook.add` opens, writes and closes the file for every line: 3.1 µs per line
    against 0.04 µs in memory (measured, 78 times slower), under the lock of the book. A
    Verbose OpenVPN tunnel writes a line per packet. Hypothesis: this slows the reading of
    the OpenVPN output and the tunnel itself; measure iperf with Verbose on and off. Fix: a
    buffered writer flushed every second and on stop.

- [ ] **UI is sent without compression**
  - One script of 1.95 MB, no gzip: 0.6 MB compressed (3.3 times less). Ant Design is
    imported whole (`app.use(Antd)`). Serve pre-compressed files and import only the used
    components.

- [ ] **No CI**
  - Tests run only on the developer machine and in the pre-commit hook. A GitHub Actions
    workflow: `go vet`, `go test -race`, `staticcheck`, the image build, the stands that
    need no browser.

- [ ] **Version and change log**
  - The image is still `VERSION=1.2.0` after DNS per outbound, recovery, log clearing and
    configuration versions 10 and 11. Bump the version, add `CHANGELOG.md` and git tags.

- [ ] **Mutation testing of the DNS generator**
  - `router/dns.go` (`arrange`, `pins`) has full statement coverage, but the order rules are
    subtle: run go-mutesting / go-gremlins on `internal/router` and `internal/config/dns.go`
    and close the surviving mutants.

- [ ] **Small findings**
  - `staticcheck` SA4006: `server_test.go` does not check the code of
    `PUT /api/users/admin` (own role).
  - `govulncheck` cannot read Go 1.27 code yet (its parser is older): check again with a
    newer release. `npm audit`: 0 vulnerabilities.
  - JavaScript has no unit tests (`dns.js`, `format.js`, `states.js`); the stands cover the
    pages only end to end.
  - Passwords of inbound users and private keys are stored in `proximity.json` in clear text
    (known limitation of 1.2.0).

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

- [x] **TOTP code can be reused**
  - Done: a code works once. It is accepted again only from the same
    user at the same address (a quick reconnect); another user or
    another address is refused and uses the next code. A Proximity
    outbound whose code was refused waits for the next code itself.
  - Left as it is: a second login of the same user from the same
    address (behind one NAT) in the same 30 seconds is accepted.

- [x] **No certificate revocation**
  - Done: the server accepts only the current client certificate
    (`peer-fingerprint`). *Regenerate* of the client certificate revokes
    the old one; other certificates of the CA are rejected too.
  - All users of an inbound share one client certificate, so one user
    cannot be revoked by certificate: disable the user instead.

- [x] **ICMP bypasses outbounds**
  - Done for clients of an OpenVPN inbound: a ping to another host is
    rejected at once (*Packet filtered*) instead of leaving through the
    host connection; an inbound that goes straight to the host is not
    limited.

- [x] **ICMP of the host in transparent mode**
  - Done: **Settings → Keep ping of the host on its routes** (on by default,
    `ping_filter`, configuration version 9). The chain `PROXIMITY-PING`
    (`iptables` and `ip6tables`) lets the echo requests of mihomo
    (`fwmark 51000`) out only to addresses whose route or the default
    target is DIRECT or HOST; others are dropped, so ping shows no answer
    instead of leaving directly. Checked with the stand `capture`
    (`make stand-capture`, 12 checks).
  - Found on 2026-11-18: `ping` of an address routed to a proxy left
    directly from the address of the host: mihomo answers an echo request
    that came into `proximity_tun` with its own request sent directly,
    without the rules. `tun.disable-icmp-forwarding: true` was not taken:
    the gVisor stack then answers every ping itself, even for an absent
    address.

- [x] **ICMP through VPN outbounds**
  - Done: ping of an IP route or of the default target with an OpenVPN or
    AmneziaWG outbound goes through the tunnel: `ip rule` 5165 / 5166
    (`ipproto icmp … lookup <table>`) in the order of the routes, and
    `MASQUERADE` of the TUN source `198.18.0.0/30` on `proximity_o+` and
    `proximity_a+`. Checked with OpenVPN and AmneziaWG on the stand
    `capture` (`rp_filter=1`).
  - Found on the way: the kernel refuses `ip rule add … ipproto 1 lookup T`
    without `to` (*File exists*) when a rule with the same priority, table
    and `to` exists, so the default target has its own priority 5166.
  - Left: ping of a fallback whose members are tunnels is dropped (the
    member in use is chosen by mihomo).

- [ ] **Ping filter: GEOIP and IP-ASN routes**
  - A GEOIP, IP-ASN, source or process route with a target that is not
    direct ends the filter with DROP: such a route cannot be checked by
    address, so ping of every later address is dropped. An `ipset` built
    from the GeoIP database would check GEOIP exactly.

- [ ] **Ping of a name with fake-IP**
  - Not checked: `ping <name>` when the host resolves through the DNS of
    the capture (fake-IP `198.18.x.x`). Which address does mihomo ping,
    and does the filter see the route of the domain? The stand has no DNS
    server for this yet.

- [ ] **`TestManager_OpenVPN` failed once**
  - Failed once in a full `go test -cover ./...` run on 2026-11-18 (0.98 s),
    passed alone and in four later full runs. The message was not saved;
    watch for it.

- [ ] **Hint for inbounds with the old defaults**
  - Inbounds created earlier keep the network `10.77.<N>.0/24` and the
    MTU 1500. Show a hint in the inbound dialog, or offer to switch.
  - Measure the fragmentation on the wire (`tcpdump` on the stand
    `pair`) with 1420 and with 1500 and write the numbers down.

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

## VPN routes

- [x] **Take the routes of a VPN connection automatically**
  - Done: the switch "Receive routes" of an OpenVPN or AmneziaWG outbound
    (off by default). The route appears at the top, is on only while the
    connection is up and has given its routes, cannot be edited, switched
    or deleted, can be moved, keeps its place after a restart, and is
    removed when the switch is off. After an upgrade a provided route that
    was on keeps working, one that was off is removed.
  - Checked on the stand `pair` with OpenVPN (`make stand-tunnel`) and on
    the stand `capture` with AmneziaWG (`make stand-capture`).
  - Original task:
  - Add the checkbox "Receive routes" to a VPN outbound, right beside
    "Use DNS servers pushed by VPN". The routes are what the VPN server
    announces: networks and single IP addresses.
  - With the checkbox on, keep a route in the list of routes that cannot
    be deleted and is filled automatically with the routes of this VPN
    connection.
  - Follow the state of the connection:
    - the connection fails or stops: the route is switched off;
    - the connection is up: wait until the server has given its routes,
      update the route, then switch it on.
  - A new route of this kind is added at the top of the list, but the
    user can move it up and down like any other route.
  - The position is saved: after a restart the route is where the user
    left it.
  - With the checkbox off there is no such route.

## Configuration

- [x] **Start with a partly invalid configuration**
  - Done: a block that cannot be read or checked is dropped, the rest
    starts. The message at the top of the page lists what was dropped
    and why; the file as it was is kept as
    `proximity.invalid-<time>.json`. A route or an inbound that points
    to a dropped element is kept and reported. Original task:
  - When Proximity starts and finds something wrong in the configuration,
    it must still start with what is valid.
  - A block of settings with incorrect data (an outbound, inbound, route,
    fallback, user or a group of settings) is dropped; the rest is applied.
  - After the start show a message at the top, in the header: Proximity
    started, but there were errors, with the list of the configuration
    elements that were dropped and the reason for each.
  - Decide: keep the dropped blocks in the file (so that they can be
    fixed) or remove them on the next save; what to do with references
    to a dropped element (routes, fallbacks, inbounds).

## Web UI

- [x] **Reload the open web UI when Proximity behind it was updated or restarted**
  - Done: every answer carries `X-Proximity-Instance`, a mark that is new
    after every start. When the mark changes, the page warns that unsaved
    changes, also in open dialogs, are lost and reloads itself in 5 seconds
    ("Reload now" at once): an update goes before the input of the user.
    Checked in a browser: `make stand-reload`.
  - Original task:
  - An open page keeps running the old JavaScript after the container was
    rebuilt (a new version) or just restarted; it must notice this and
    reload itself.
  - What to compare with the values remembered at page load:
    - the Proximity version (`version` of `GET /api/session`);
    - a unique ID of the running instance: a random value generated at
      every start of the process. There is no such ID in the API now;
      add it (for example `instance` in `GET /api/session` or in
      `GET /api/dashboard`, which the page already polls every 3 seconds).
  - Choose: the version alone misses a restart and a rebuild without a
    version bump; the instance ID alone covers both cases.
  - On a mismatch reload the page automatically. Decide what to do with
    an open dialog that has unsaved changes and with the time when the
    server does not answer (the container is still starting).

- [x] **Reload with unsaved input outside dialogs**
  - Done: dropped by the decision of 2026-11-18. An update is rare and goes
    before the input of the user: the page warns that unsaved changes, on
    forms and in dialogs, are lost and reloads itself in 5 seconds.
  - Original task: the automatic reload waits only for dialogs. Values typed on the
    Settings page (it is a form, not a dialog) are lost when Proximity
    restarts at that moment. Track changed forms and show the notice
    instead of reloading.

## Test stands

- [ ] **Prepare ready stands for the usual tests**
  - Done so far: `scripts/stands/pair` (two linked instances: security
    checks of the OpenVPN inbound, checks of the link — routes, token,
    disabled user), `scripts/stands/reload` (an open page in a browser),
    `scripts/stands/broken-config` (start with a damaged configuration),
    the time server for a network without Internet (`STAND_OFFLINE=1`)
    and the stand of the screenshots. Still to do: an external OpenVPN
    client and an external OpenVPN server (not Proximity), latency,
    recovery of lost connections.
  - A stand is a ready environment (containers, networks, sample data,
    helper scripts) that a test or a task starts with one command, so
    nobody has to work out again how it is built.
  - Make several stands for different tests and tasks, for example:
    - OpenVPN inbound with an external client (certificates, passwords,
      TOTP);
    - OpenVPN outbound to an external server;
    - two linked Proximity instances (client and server);
    - latency measurements through every inbound, route and outbound;
    - recovery of lost connections (a server that goes away and comes
      back);
    - a network without Internet and without NTP.
  - Keep them in `scripts/stands/<stand>/`, one folder per stand, with a
    start script and a short description of what the stand does.
  - Describe every stand briefly in `AGENTS.md`.
  - Rule for the future, written in `AGENTS.md`: a new kind of test gets
    its own ready stand in `scripts/stands/` and its line in `AGENTS.md`.
  - The stand of the screenshots (`scripts/screenshots.sh`) already
    exists; decide whether to move it under `scripts/stands/`.

- [x] **Stand with AmneziaWG**
  - Done: the stand `capture` has an AmneziaWG server
    (`proximity-stand-awg`: amneziawg-go from the image, Jc/S1/S2/H1-H4,
    masquerade). Checked: the outbound connects with the obfuscation, the
    route from `AllowedIPs` appears first and is on, TCP and ping of it go
    through the tunnel, the route is switched off when the outbound
    stops and ping then follows the default target (29 checks).
  - Original task: no stand had an AmneziaWG server: "Receive routes" of
    an AmneziaWG outbound (the networks of `AllowedIPs`) was covered only
    by unit tests.

## Build

- [x] **One Go version for the tests and the image**
  - Done: `go 1.27.0` and `toolchain go1.27.4` in `go.mod`, the image builds
    with `golang:1.27.4-bookworm`; an older local `go` downloads 1.27.4 itself.
  - `go.mod` says `go 1.24`, the image builds with `golang:1.25-bookworm`,
    the user's machine has Go 1.27, where `encoding/json` works on jsonv2
    and prints other error texts (found by `make test` on 2026-11-18).
  - Pin one version (`toolchain` in `go.mod` and `GO_IMAGE`), or run the
    tests with both versions in `make test`.

## Performance

- [ ] **Investigate high latency when routing traffic through Proximity**
  - Found and fixed: OpenVPN over TCP lacked `TCP_NODELAY` — 168 ms → 2 ms per HTTP request.
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
