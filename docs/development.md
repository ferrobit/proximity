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
│   ├── link/               endpoint drivers, monitoring, routing tables, transparent capture
│   ├── router/             mihomo config generator, mihomo process, traffic tracker
│   ├── rules/              filter parsing, normalization, consistency checks
│   ├── server/             HTTP API and control loop
│   └── wgconf/             WireGuard / AmneziaWG config parser, vpn:// import
├── web/                    Vue 3 + Ant Design Vue UI (Vite); dist/ is embedded into the binary
├── scripts/
│   ├── proximity.sh        compose wrapper for Linux and macOS
│   ├── proximity.ps1       compose wrapper for Windows
│   └── githooks/pre-commit rebuilds the UI and binary before commit
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
| Docker | with Compose v2, for the image |
| For running tunnels locally | `ip` (iproute2), `ping`, `openvpn`, `amneziawg-go`, `awg`, `oathtool`, root or `CAP_NET_ADMIN` |

## Build

```bash
make web      # npm ci + vite build → web/dist
make build    # CGO_ENABLED=0 go build → bin/proximity
make all      # both
make vet      # gofmt -l + go vet
make image    # docker image only
make up       # build image and start the container
make logs     # follow container logs
make down     # stop
```

`web/dist` is committed: the Go binary embeds it with `//go:embed all:dist`, so
`go build` works without Node.js. Enable the pre-commit hook to keep it fresh:

```bash
git config core.hooksPath scripts/githooks
```

## Run locally without Docker

The binary needs `mihomo` and, for tunnels, root privileges and the helper tools.

```bash
make all
sudo bin/proximity \
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
cd web
npm ci
npm run dev        # Vite dev server; /api is proxied to http://127.0.0.1:8899
```

Run the Go binary on port 8899 at the same time. Main files:

| File | Purpose |
|---|---|
| `src/App.vue` | layout, header statistics, navigation, sign-in and first-run dialogs, theme |
| `src/api.js` | fetch wrapper, session state, role helpers |
| `src/views/*.vue` | one view per section; `EndpointDialog.vue` is the endpoint editor |
| `src/components/TrafficChart.vue` | dependency-free SVG throughput chart |
| `src/components/LogView.vue` | auto-refreshing, auto-scrolling log view |

## Tests

```bash
make test     # go test ./...
make cover    # with coverage per package
go test -race ./...
```

Current state (version 1.1.0): **87 tests, all passing, 92.9% total statement coverage.**

| Package | Coverage |
|---|---|
| `cmd/proximity` | 80.5% |
| `internal/auth` | 94.1% |
| `internal/config` | 96.0% |
| `internal/link` | 86.3% |
| `internal/router` | 95.7% |
| `internal/rules` | 99.1% |
| `internal/server` | 93.9% |
| `internal/wgconf` | 97.9% |

Tests use fake tools (`ip`, `openvpn`, `ping` …) and a fake mihomo, so they run without
root and without network access. Assertions use `testify/require`.

## Code style

- `gofmt` only; no other formatters.
- Errors are wrapped at every level: `fmt.Errorf("failed to <verb> <object> '%v': %w", name, err)`.
- Full-word identifiers (`index`, `endpoint`, `result`), no single-letter names.
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
2. `make all` — commit the updated `web/dist`.
3. Bump `VERSION` in `Makefile`, `Dockerfile` and the default `PROXIMITY_VERSION` in the
   compose files.
4. Update the *Project status* section in `README.md` and screenshots if the UI changed.
5. `make up` on a clean settings folder: first-run dialog, one endpoint of each type,
   transparent mode on Linux.
6. Tag the release.
