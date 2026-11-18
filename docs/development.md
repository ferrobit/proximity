# Development

- [Project layout](#project-layout)
- [Prerequisites](#prerequisites)
- [Build](#build)
- [Run locally without Docker](#run-locally-without-docker)
- [UI development](#ui-development)
- [Screenshots](#screenshots)
- [Tests](#tests)
- [Test stands](#test-stands)
- [Code style](#code-style)
- [Updating bundled components](#updating-bundled-components)
- [Release checklist](#release-checklist)

## Project layout

```text
proximity/
├── cmd/proximity/          entry point (flags, env, `health` and `hook` subcommands)
├── internal/
│   ├── auth/               password hashing, session tokens, bootstrap administrator
│   ├── clock/              SNTP client, host clock check, Proximity's own time
│   ├── config/             model, validation, renames, JSON store, backup
│   ├── link/               outbound drivers, monitoring, routing tables, transparent capture
│   ├── router/             mihomo config generator, mihomo process, traffic tracker
│   ├── rules/              route parsing, normalization, consistency checks
│   ├── server/             HTTP API and control loop
│   └── wgconf/             WireGuard / AmneziaWG config parser, vpn:// import
├── web/                    Vue 3 + Ant Design Vue UI (Vite); dist/ is built, embedded, not committed
├── scripts/
│   ├── proximity.sh        compose wrapper for Linux and macOS
│   ├── proximity.ps1       compose wrapper for Windows
│   ├── screenshots.sh      retakes the pictures of docs/images (make screenshots)
│   ├── screenshots/        its stand, sample data, headless browser script and npm packages list
│   ├── stands/             ready stands for end-to-end checks (see Test stands)
│   └── githooks/pre-commit make vet build before commit
├── docs/                   documentation and screenshots
├── AGENTS.md               notes for AI agents: what failed and what to do instead
├── Dockerfile              multi-stage image build
├── docker-compose.yml          Linux, host network, transparent capture
├── docker-compose.bridge.yml   macOS / Windows / Linux without capture
├── Makefile                    only includes makefile.mk
└── makefile.mk                 all make targets
```

## Prerequisites

| Tool | Version |
|---|---|
| Go | 1.27.4 (`toolchain` in `go.mod`; an older `go` downloads it itself) |
| Node.js | 22 |
| rsync | any, for `make` |
| Docker | with Compose v2, for the image |
| A Chromium-based browser | Chrome, Chromium, Brave, Thorium, Edge, Vivaldi or Yandex Browser, for `make screenshots` |
| `/dev/net/tun` for Docker | for `make screenshots`: the stand runs a VPN tunnel |
| For running tunnels locally | `ip` (iproute2), `ping`, `openvpn`, `amneziawg-go`, `awg`, `oathtool`, root or `CAP_NET_ADMIN` |

## Build

Everything `make` builds, downloads or caches goes to `../proximity-build` — a folder
next to the repository (override with `make BUILD=/path`). The repository itself is only
read: `make` copies it with `rsync` (without `.git`, `docs`, `bin`, `web/node_modules`,
`web/dist`) and works on the copy.

```
proximity-build/
├── source/          copy of the repository; web/node_modules and web/dist live here
├── bin/proximity    the binary
├── cache/           Go build and module caches, npm cache (GOCACHE, GOMODCACHE, npm_config_cache)
├── tmp/             TMPDIR / GOTMPDIR of builds and tests
├── screenshots/     node_modules of make screenshots
└── coverage.out     make cover
```

```bash
make web      # sync + npm ci (only when package-lock.json changed) + vite build
make build    # sync + CGO_ENABLED=0 go build → ../proximity-build/bin/proximity
make all      # both
make vet      # gofmt -l + go vet
make test     # go test ./...
make cover    # go test with coverage
make dev      # Vite dev server on the copy; edits are synced every second
make clean    # remove ../proximity-build
make image    # docker image only
make up       # build image and start the container
make logs     # follow container logs
make screenshots   # retake the pictures in docs/images
make screenshot <picture>   # retake one picture
make stand-security   # end-to-end checks of the OpenVPN inbound security
make stand-config     # end-to-end check of a start with a damaged configuration
make stand-capture    # end-to-end checks of TCP and ping of a host with transparent capture
make stand-dns        # end-to-end checks of DNS: which server is asked and where the query leaves
make down     # stop
```

`web/dist` is **not** committed: only the empty placeholder `web/dist/.gitkeep` is, so
`go build`, `go vet` and `go test` work without Node.js (also when run by hand or by the
IDE). Such a binary answers the web UI with *503 web UI is not built*; `make all` builds
one with the UI.

The Docker image does not use the local `web/dist` at all (`.dockerignore` excludes it):
the `web` stage of the `Dockerfile` runs `npm ci` and `vite build` inside the builder and
the `build` stage embeds the result. `docker compose build` / `make up` never change
files in the working tree.

Optional pre-commit hook (`make vet build`; no UI build):

```bash
git config core.hooksPath scripts/githooks
```

## Run locally without Docker

The binary needs `mihomo` and, for tunnels, root privileges and the helper tools.

```bash
make all
sudo ../proximity-build/bin/proximity \
  --listen 127.0.0.1:8899 \
  --config /tmp/proximity/config \
  --run /tmp/proximity/run \
  --geodata /path/to/geodata \
  --mihomo /path/to/mihomo
```

Without `PROXIMITY_NETWORK=host` Proximity works in bridge mode: no transparent capture,
programs use the proxy port. Install `libfaketime` too (Debian/Ubuntu package
`libfaketime`): OpenVPN needs it when the host clock is wrong.

> [!NOTE]
> The screenshots in this repository are taken by `make screenshots` from a stand of two
> containers: real mihomo, a real OpenVPN tunnel, SOCKS5/HTTP proxies and generated
> traffic, see [Screenshots](#screenshots). Host names like `corp.example` are
> placeholders.

## UI development

```bash
make dev           # Vite dev server on http://localhost:5173; /api is proxied to http://127.0.0.1:8899
```

`make dev` runs Vite in `../proximity-build/source/web` and copies changed files from the
repository every second, so edits in `web/src` reload the page. Run the Go binary on port
8899 at the same time. Main files:

| File | Purpose |
|---|---|
| `src/App.vue` | layout, header statistics, navigation, sign-in and first-run dialogs, theme |
| `src/api.js` | fetch wrapper, session state, role helpers |
| `src/views/*.vue` | one view per section: `Dashboard.vue` has the Overview, Connection map, Outbounds and Unreachable routes tabs, `Routes.vue` shows `Fallbacks.vue` at the bottom, `Settings.vue` shows `Users.vue` as its third section; `OutboundDialog.vue`, `InboundDialog.vue`, `RouteDialog.vue` and `FallbackDialog.vue` are the editors, shared by the section pages and the connection map |
| `src/components/ConnectionMap.vue` | connection map: rendering, hover highlight, popups, edit dialogs, zoom, fit to screen and full screen |
| `src/components/RuleEditor.vue` | route parameters editor: highlighting overlay, line numbers, load, save and sort |
| `src/components/logSyntax.js` | log line parser for `LogView.vue`: time stamp, severity, IP addresses, quoted text, level words, links |
| `src/components/RulePreview.vue` | highlighted, numbered list of the generated router rules with **Copy** |
| `src/components/CodeEditor.vue`, `vpnSyntax.js` | text editor with line numbers and a highlighting overlay for OpenVPN configurations, AmneziaWG / WireGuard configurations, PEM keys and certificates and pushed routes; used in the outbound and inbound dialogs and the client configuration preview |
| `src/components/ConfigView.vue`, `configSyntax.js` | generated mihomo configuration: JSON and YAML (js-yaml) views with line-based highlighting (keys, strings, numbers, addresses, domains, targets, rules, secrets), **Hide passwords**, **Copy** and **Save** |
| `src/components/ruleSyntax.js` | tokenizers for route parameters and generated router rules, the sort used by the editor |
| `src/components/rule-colors.css`, `ruleTheme.js` | token colors for the light and dark themes shared by the editor and the preview |
| `src/clipboard.js` | copy to the clipboard with a fallback for plain HTTP (no secure context) |
| `src/states.js` | state colors shared by the connection map, state badges and tables: state → tone (`good`, `busy`, `warn`, `bad`, `off`), tone → theme token, row classes; `components/TonePalette.vue` publishes the tones as CSS variables `--tone-*` for the current theme |
| `src/components/topology.js` | builds the map graph from the API data and lays it out in columns (pure functions) |
| `src/components/TrafficChart.vue` | dependency-free SVG throughput chart |
| `src/components/ConnectionsChart.vue` | dependency-free SVG chart of active connections |
| `src/components/history.js` | shared `/api/history` loader for both Dashboard charts |
| `src/components/LogView.vue` | auto-refreshing, auto-scrolling log view |

## Screenshots

```bash
make screenshots                                    # all 30 pictures of the documentation
make screenshots ONLY=static                        # dialogs only, no traffic wait
make screenshot outbound-monitoring                 # one picture
make screenshot outbound-monitoring inbound-users   # some pictures
```

`make screenshot <picture> ...` takes the pictures named after the goal; a picture is
named as its file in `docs/images`, `.png` may be omitted, `static` and `live` name the
two groups. `make screenshots` takes all pictures or those of `ONLY`. Both run
`scripts/screenshots.sh` (it also works by itself, with the picture names as arguments).

The script builds the image `proximity-screenshots` from the working tree and starts a
throwaway stand in its own Docker network (`10.213.77.0/24`):

| Container | Role |
|---|---|
| `proximity-screenshots` | the instance in the pictures; the UI, the proxy port and the SOCKS5 inbound are published on free ports of `127.0.0.1` |
| `proximity-screenshots-peer` | a second instance that plays the far side: its SOCKS5 inbound is `de.proxy.example:1080`, its proxy port is `us.proxy.example:3128`, its OpenVPN inbound is `vpn.corp.example:1194`, its web UI is every site that the traffic goes to, and it answers for `1.1.1.1:443`, the target of the connectivity checks |

Both instances get the sample data of `scripts/screenshots/seed.json` through the API
(outbounds, a fallback, routes, inbounds, users; the keys of the far-side VPN are fixed
there, so the client configuration is the same in every run). `stand.mjs` sends traffic
through the proxy port and the SOCKS5 inbound to names that the routes send to different
outbounds; all names are aliases of the far side, so nothing leaves the stand. A headless
browser then opens every page and dialog. The stand is removed at the end; a running
Proximity and its settings are not touched.

Every picture is compared with the file in `docs/images` pixel by pixel. A file is written
only when the pixels changed (`updated`) or the file did not exist (`new`); the same
picture in other bytes (another browser version, added metadata) is left as it is
(`unchanged`), so Git sees only real changes. The pictures are 2x.

| Group | Pictures | Behaviour |
|---|---|---|
| `static` | `outbound-general`, `outbound-socks5`, `outbound-http`, `outbound-host`, `outbound-openvpn`, `outbound-openvpn-auth`, `outbound-monitoring`, `outbound-amneziawg`, `inbound-openvpn`, `inbound-users`, `inbound-export`, `route-dialog`, `route-editor-ip`, `fallback-dialog`, `router-config` | dialogs; the page behind is hidden, the same UI gives the same pixels on the same machine |
| `live` | `setup`, `dashboard`, `dashboard-dark`, `throughput`, `connection-map`, `connection-map-dark`, `connection-map-popover`, `outbounds`, `outbounds-dark`, `outbound-add-menu`, `outbound-log`, `routes`, `fallbacks`, `inbounds`, `settings` | states, traffic, time and logs: differ in every run, so every run rewrites them |

Live pictures wait until the stand has 5 minutes of traffic (the charts show 5 minutes),
so a full run takes about 7 minutes; the static group takes about 1.5 minutes. Fonts
differ between machines, so the first run on another machine rewrites all pictures.
`logo.png`, `social-preview.png` and the `.svg` files are drawn by hand and are not taken
by the script.

A new picture is one line in the `shots` list of `scripts/screenshots/screenshots.mjs`
(its kind is one of the functions in `takers`) and, when it needs other data, an entry in
`seed.json`.

The browser is looked up by name in `PATH`, as `which` does: `google-chrome`, `chromium`,
`chromium-browser`, `brave-browser`, `brave`, `thorium-browser`, `microsoft-edge`, `vivaldi`,
`yandex-browser` and their `-stable` / `-beta` names, in this order (the full list is
`browsers` in `browser.mjs`). When none is in `PATH`, the usual install paths are tried
(`/opt/...`, `/snap/bin`, `/Applications` — the list `applications`). The script checks the
browser before the image build. Any other browser on the Chromium engine works through
`CHROME`.

| Variable | Meaning |
|---|---|
| `PROXIMITY_IMAGE` | use this ready image instead of building one |
| `CHROME` | name or path of the browser to use instead of the one found |
| `SCREENSHOT_TRAFFIC` | seconds of traffic before the live pictures (default 300) |
| `SCREENSHOT_SUBNET` | subnet of the stand (default `10.213.77.0/24`) |
| `SCREENSHOT_NETWORK` | use this existing Docker network instead of creating one |
| `SCREENSHOT_OUTPUT` | folder for the pictures (default `docs/images`) |
| `SCREENSHOT_TOOLS` | folder for `node_modules` (default `../proximity-build/screenshots`, `make` follows `BUILD`) |

> [!NOTE]
> A picture that must have changed because the code or the layout of its dialog changed
> may be replaced right away: there is no need to open the new picture and to compare it
> with the old one. Look at a picture only when it is not clear whether it had to change.

## Tests

```bash
make test     # go test ./...
make cover    # with coverage per package; profile in ../proximity-build/coverage.out
make coverage-docs    # the same, then scripts/coverage.sh writes the numbers into the documents
go test -race ./...   # by hand: uses the default Go caches
```

The header of `README.md` (two badges and a line), its *Project status* and the table
below show the number of tests and the coverage. Run `make coverage-docs` after every
change of the Go code: it replaces these numbers and changes nothing else.

Current state (version 1.2.0): **146 tests, all passing, 94.5% total statement coverage.**

| Package | Coverage |
|---|---|
| `cmd/proximity` | 81.4% |
| `internal/auth` | 94.1% |
| `internal/clock` | 97.0% |
| `internal/config` | 96.3% |
| `internal/link` | 91.0% |
| `internal/router` | 97.3% |
| `internal/rules` | 99.1% |
| `internal/server` | 95.0% |
| `internal/wgconf` | 97.5% |

Tests use fake tools (`ip`, `openvpn`, `iptables`, `ping` …) and a fake mihomo, so they run
without root and without network access. `TestManager_Inbounds` binds one free TCP port in
7800…7898 on the loopback address. Assertions use `testify/require`.

## Test stands

A stand is a ready environment for end-to-end checks: throwaway containers in their own
Docker network (`10.213.78.0/24`), started with one command and removed at the end. A
stand does not use the host network and does not touch a running Proximity.

```bash
make stand-security                     # stand "pair": 16 checks, about 1 minute
make stand-tunnel                       # stand "pair": 24 checks, about 20 seconds
make stand-reload                       # stand "reload": 10 checks, about 25 seconds, needs a browser
make stand-config                       # stand "broken-config": 17 checks, about 5 seconds
make stand-capture                      # stand "capture": 29 checks, about 45 seconds
make stand-dns                          # stand "dns": 25 checks, about 1 minute
bash scripts/stands/pair/stand.sh up    # only start the stand; "down" removes it
```

| Stand | What it is | Checks |
|---|---|---|
| `pair` | two linked instances: `proximity-stand-a` (client side) and `proximity-stand-b` (server side, also answers for `1.1.1.1:443`) | `make stand-security` (`security.sh`): single use of one-time codes, rejection of an old client certificate, ping of clients. `make stand-tunnel` (`tunnel.sh`): default network and MTU of a new inbound, the route of *Receive routes* (appears, follows the connection, can be moved, keeps its place after a restart), the token after a restart of the inbound, a disabled user, the instance mark |
| `reload` | one instance on the fixed port `127.0.0.1:18899` (`STAND_PORT`) and a headless browser with its page open; uses the browser and the node modules of the screenshots | `make stand-reload` (`check.sh`): after a restart of the container the page warns that unsaved changes are lost and reloads itself in 5 seconds, also with an open dialog; "Reload now" reloads at once |
| `broken-config` | one instance started with the damaged `config.json` of the stand | `make stand-config` (`check.sh`): what is dropped, what is kept, the copy of the file, the report and its dismissal |
| `capture` | a "host" (a network namespace with `rp_filter=1` and its default route via `proximity-stand-wan`), Proximity in its network with capture on (`PROXIMITY_NETWORK=host`), an exit instance with SOCKS5 and OpenVPN inbounds, an AmneziaWG server (`proximity-stand-awg`), a local time server and the "Internet" `proximity-stand-wan` that owns `1.1.1.1` (route `IP-CIDR` to SOCKS5), `1.1.1.2` (no route), `1.1.1.3` (route to OpenVPN) and `1.1.1.4` (route from the `AllowedIPs` of AmneziaWG) and counts the packets by source | `make stand-capture` (`check.sh`): TCP of each address leaves where its route says; ping of the SOCKS5 route does not leave and gets no false answer, ping without a route gets real answers, ping of the tunnel route goes through the tunnel; the AmneziaWG outbound connects with its obfuscation, *Receive routes* adds the networks of `AllowedIPs` first and switches the route off when the outbound stops, TCP and ping of that route go through the tunnel; the default target SOCKS5 drops ping, the default tunnel carries it and LAN ping stays on the LAN; *Keep ping of the host on its routes* off removes everything |
| `dns` | the "host" of `capture` with `nameserver 1.1.1.1`, Proximity in its network with capture on, an exit instance with two SOCKS5 inbounds (with and without UDP) and an OpenVPN inbound, the "Internet" `proximity-stand-wan` with `1.1.1.1` (the resolver of the system), `1.1.1.53` (the DNS server given to the outbounds) and `1.1.1.2`, and `proximity-stand-dns`: a DNS server on both addresses (`server.py`) that prints every query with the address it came from | `make stand-dns` (`check.sh`): outbounds whose servers are given by name (`exit.stand.test`, `vpn.stand.test`) connect, the `remote` is replaced by the real address, the server name of a proxy is never asked through the proxy (the pin in `nameserver-policy`), the time servers are resolved by the router, an application gets a placeholder address; the domain of a route is asked from the server of its outbound through the outbound (UDP, TCP for a proxy without UDP, the far side of a tunnel, the exit *Host*), the default servers of Settings and the DNS of the default target; the answers pass `rp_filter=1`; no warnings |
| screenshots | two linked instances with sample data and traffic, see [Screenshots](#screenshots) | — |

`scripts/stands/lib.sh` holds the common parts (image, network, instance, removal),
`lib.mjs` the helpers of the check scripts (API client, `docker`, `PASS` / `FAIL` lines;
the exit code is 0 only when all checks passed). Variables: `PROXIMITY_IMAGE` (use a
ready image instead of building `proximity-stand`), `STAND_SUBNET`, `STAND_KEEP=1` (leave
the stand running, the UI addresses are printed), `STAND_OFFLINE=1` (no Internet: a small
time server, `time/server.py`, answers for the default NTP servers, because OpenVPN
starts only with checked time). A new kind of check gets its own stand in
`scripts/stands/<stand>/` and a line in `AGENTS.md`.

## Code style

- `gofmt` only; no other formatters.
- Errors are wrapped at every level: `fmt.Errorf("failed to <verb> <object> '%v': %w", name, err)`.
- Full-word identifiers (`index`, `outbound`, `result`), no single-letter names.
- No comments except `// NOTE:` and `// TODO:` where the reason is not obvious.
- Tests: `Test<API>_<Case>`, `require` from testify, sub-cases as plain `{ … }` blocks.

## Updating bundled components

Versions are build arguments in the `Dockerfile`:

| Argument | Current |
|---|---|
| `MIHOMO_IMAGE` | `metacubex/mihomo:v1.19.34` |
| `AWG_GO_VERSION` | `v3.1.20260828` |
| `AWG_TOOLS_VERSION` | `v3.1.20260812` |
| `GO_IMAGE` | `golang:1.27.4-bookworm` |
| `NODE_IMAGE` | `node:22-bookworm-slim` |
| `BASE_IMAGE` | `debian:bookworm-slim` |

Override without editing the file:

```bash
docker compose build --build-arg MIHOMO_IMAGE=metacubex/mihomo:v1.19.35
```

GeoIP/GeoSite databases come from the mihomo image and are copied into the settings folder
only if missing; delete `mihomo/geoip.metadb` and `mihomo/geosite.dat` there to pick up
new ones after an upgrade.

## Release checklist

1. `make vet test` — clean.
2. `make all` — the UI and the binary build without errors.
3. Bump `VERSION` in `makefile.mk`, `Dockerfile` and the default `PROXIMITY_VERSION` in the
   compose files.
4. Update the *Project status* section in `README.md` and screenshots if the UI changed
   (`make screenshots` retakes them).
5. `make up` on a clean settings folder: first-run dialog, one outbound of each type,
   transparent mode on Linux.
6. Tag the release.
