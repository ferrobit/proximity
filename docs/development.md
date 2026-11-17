# Development

- [Project layout](#project-layout)
- [Prerequisites](#prerequisites)
- [Build](#build)
- [Run locally without Docker](#run-locally-without-docker)
- [UI development](#ui-development)
- [Tests](#tests)
- [Code style](#code-style)
- [Updating bundled components](#updating-bundled-components)
- [Release checklist](#release-checklist)

## Project layout

```text
proximity/
├── cmd/proximity/          entry point (flags, env, `health` and `hook` subcommands)
├── internal/
│   ├── auth/               password hashing, session tokens, bootstrap administrator
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
│   └── githooks/pre-commit make vet build before commit
├── docs/                   documentation and screenshots
├── Dockerfile              multi-stage image build
├── docker-compose.yml          Linux, host network, transparent capture
├── docker-compose.bridge.yml   macOS / Windows / Linux without capture
└── Makefile
```

## Prerequisites

| Tool | Version |
|---|---|
| Go | 1.24+ (the image builds with 1.25) |
| Node.js | 22 |
| rsync | any, for `make` |
| Docker | with Compose v2, for the image |
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
programs use the proxy port.

> [!NOTE]
> The screenshots in this repository were taken from exactly such a local run: real
> mihomo v1.19.34, real OpenVPN and AmneziaWG tunnels to servers in a separate network
> namespace, local SOCKS5/HTTP proxies and generated traffic. Host names like
> `corp.example` are placeholders.

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
| `src/components/ruleSyntax.js` | tokenizers for route parameters and generated router rules, the sort used by the editor |
| `src/components/rule-colors.css`, `ruleTheme.js` | token colors for the light and dark themes shared by the editor and the preview |
| `src/clipboard.js` | copy to the clipboard with a fallback for plain HTTP (no secure context) |
| `src/components/topology.js` | builds the map graph from the API data and lays it out in columns (pure functions) |
| `src/components/TrafficChart.vue` | dependency-free SVG throughput chart |
| `src/components/ConnectionsChart.vue` | dependency-free SVG chart of active connections |
| `src/components/history.js` | shared `/api/history` loader for both Dashboard charts |
| `src/components/LogView.vue` | auto-refreshing, auto-scrolling log view |

## Tests

```bash
make test     # go test ./...
make cover    # with coverage per package; profile in ../proximity-build/coverage.out
go test -race ./...   # by hand: uses the default Go caches
```

Current state (version 1.2.0): **105 tests, all passing, 93.5% total statement coverage.**

| Package | Coverage |
|---|---|
| `cmd/proximity` | 81.4% |
| `internal/auth` | 94.1% |
| `internal/config` | 95.8% |
| `internal/link` | 89.4% |
| `internal/router` | 96.5% |
| `internal/rules` | 99.1% |
| `internal/server` | 94.2% |
| `internal/wgconf` | 97.9% |

Tests use fake tools (`ip`, `openvpn`, `iptables`, `ping` …) and a fake mihomo, so they run
without root and without network access. `TestManager_Inbounds` binds one free TCP port in
7800…7898 on the loopback address. `TestManager_Capture` expects that the machine has no
`proximity_tun` interface, so stop a locally running Proximity before `make test`. Assertions use `testify/require`.

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
| `GO_IMAGE` | `golang:1.25-bookworm` |
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
3. Bump `VERSION` in `Makefile`, `Dockerfile` and the default `PROXIMITY_VERSION` in the
   compose files.
4. Update the *Project status* section in `README.md` and screenshots if the UI changed.
5. `make up` on a clean settings folder: first-run dialog, one outbound of each type,
   transparent mode on Linux.
6. Tag the release.
