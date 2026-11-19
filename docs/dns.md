# DNS

Where a name is resolved matters as much as where the traffic goes: a name asked from the
provider of the host leaks what you open, and a name that exists only behind a VPN does not
resolve anywhere else. Proximity asks every name where its traffic leaves.

- [The rule](#the-rule)
- [Servers of an outbound](#servers-of-an-outbound)
- [Which routes bind names](#which-routes-bind-names)
- [Servers given by name](#servers-given-by-name)
- [Names in the settings](#names-in-the-settings)
- [Placeholder addresses (fake-IP)](#placeholder-addresses-fake-ip)
- [OpenVPN inbounds and DNS](#openvpn-inbounds-and-dns)
- [Check it](#check-it)

## The rule

A name is resolved by the DNS servers of the outbound that its route leads to, and the
query leaves through that outbound:

| The route of the name goes to | Who resolves it |
|---|---|
| An outbound with servers on its **DNS** tab | those servers, through the outbound (or the exit chosen on the tab) |
| A VPN with *Use DNS servers pushed by VPN* / *…from configuration* | the servers of the VPN, through the tunnel |
| A SOCKS5 or HTTP proxy without servers of its own | the proxy itself (the name is sent as is) |
| `DIRECT`, HOST, a VPN without DNS | the default servers of **Settings → Router → DNS** (empty: the resolver of the container) |
| A fallback | the servers of its first working member |
| No route (*Everything else*) | the servers of the default target, if it has its own; else the default servers |

The first route that matches a name decides, exactly as for the traffic. In transparent
mode (Linux) applications get a placeholder address at once and the real name is resolved
when the connection is made — by the side above.

## Servers of an outbound

Every outbound has a **DNS** tab (see the [user guide](user-guide.md#dns)):

| Form | Meaning |
|---|---|
| `8.8.8.8`, `8.8.8.8:5353` | plain DNS (UDP; over TCP where the outbound has no UDP) |
| `tcp://8.8.8.8` | DNS over TCP |
| `tls://1.1.1.1`, `tls://dns.example` | DNS over TLS (port 853) |
| `https://dns.google/dns-query` | DNS over HTTPS |
| `quic://dns.adguard-dns.com` | DNS over QUIC |

- All servers of an outbound are asked at once and the first answer wins: next to a plain
  server a DNS over TLS / HTTPS server answers only when the plain one is slower or fails.
  Leave only the encrypted servers when the queries must not go in clear.
- A SOCKS5 proxy without *UDP relay* and an HTTP proxy carry no UDP: plain servers are
  asked over TCP, `udp://` and `quic://` servers are skipped with a warning. Many HTTP
  proxies open only port 443: use DNS over HTTPS there.
- **Send the queries through** picks another exit for the queries: **Host**, **Router**
  (by the routes), another outbound or a fallback.

## Which routes bind names

All domain directives bind their names to the DNS of the target:

| Directive | Example | Bound names |
|---|---|---|
| `DOMAIN` | `wiki.corp.example` | this name |
| `DOMAIN-SUFFIX` | `corp.example` | the name and every subdomain |
| `DOMAIN-KEYWORD` | `corp` | every name that contains the word |
| `DOMAIN-WILDCARD` | `*.corp.example` | names that fit the pattern |
| `DOMAIN-REGEX` | `^git[0-9]+\.corp\.example$` | names that match the expression |
| `GEOSITE` | `google` | the names of the category |

IP, port, process and source routes do not bind names: their names follow the next
domain route or *Everything else*. The VPN can also push its own domains (OpenVPN
`dhcp-option DOMAIN`): they go to the servers of the VPN too.

## Servers given by name

`https://dns.corp.example/dns-query` on the DNS tab of a VPN outbound: the name of the
server itself has to be resolved first, and the default servers usually do not know a name
of the VPN. Proximity resolves such a name through the servers of the **same outbound
given by address** — the servers pushed by the VPN, or another server of the tab written as
an address — and gives the address to the router. So a VPN with a pushed `10.8.0.1` and a
tab with `https://dns.corp.example/dns-query` works.

- Without a server given by address on the same outbound, the name is resolved by the
  default servers.
- Through a SOCKS5 or HTTP proxy the name of the server goes to the proxy, which resolves
  it on its side.
- The address is looked up again after every change of the configuration; the log of the
  container says `DNS server name '…' is resolved to …`.

## Names in the settings

A proxy server, an OpenVPN `remote`, an AmneziaWG `Endpoint`, a check target and a time
server may be given by name. Proximity resolves them itself by the same rules, with three
guarantees:

- the address is real, never a placeholder;
- an outbound never asks for its own server name through itself (or through an outbound
  that waits for it) — the name of a VPN server would otherwise wait for the VPN;
- the check target of AmneziaWG and the health check URL of a fallback are resolved by
  the DNS of the tunnel, where their connection goes.

## Placeholder addresses (fake-IP)

With transparent capture (and for the clients of an OpenVPN inbound) the router answers
names with addresses from `198.18.0.0/16`. Such an address stands for the name inside one
Proximity; the connection to it is matched by the name and resolved for real later.
Placeholders never leave the router as real targets: a name of the settings that gets only
placeholder answers is refused with *only placeholder (fake-IP) addresses*.

## OpenVPN inbounds and DNS

An OpenVPN inbound pushes a DNS server to its clients:

| Field *DNS servers* of the inbound | What clients get |
|---|---|
| empty (default) | `198.18.0.2`, the resolver of Proximity: placeholder answers, so the domain routes work for the clients |
| addresses | these servers; their answers are real |

A client that asks any other server gets a real answer too: only the queries to
`198.18.0.2` are taken by the router (with host capture on, every query of the host is
taken). When **another Proximity** uses the inbound as its tunnel, it resolves the names
of its settings through the tunnel and needs real answers: it asks the DNS servers of the
inbound, or fill in real servers in the inbound.

## Check it

- **Tools → Address check** shows the answer of the router and the answer of every server
  of every outbound, asked through the outbound ([user guide](user-guide.md#address-check)).
- **Routes → Router configuration** has `dns.nameserver-policy` (which servers a name gets,
  `server#exit`), `rule-providers` (the sets of keyword, wildcard and regex routes) and
  `hosts` (resolved names of servers).
- The outbounds table shows the servers of every outbound as `DNS …`.
- How the configuration is built: [Architecture → DNS](architecture.md#dns). Problems:
  [Troubleshooting](troubleshooting.md#intranet-names-do-not-resolve).
