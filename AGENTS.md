# Notes for AI agents

Terse on purpose. One line per trap. Add a line when something new fails.

## Session start
- The user's copy is written only by the agent. Do not re-read the project: list
  folders, compare `mtimeMs`/size with the last write, stage only newer files.
- `device_list_dir` on the root with `recursive` walks `.git/objects` and hits the
  2000-entry cap: list `internal`, `web/src`, `scripts`, `docs` separately.
- Write back with `expectedMtimeMs`; check with one listing. Many PNG files in one call hit
  the wall-clock timeout: write pictures and code in separate calls.
- No shell on the user's machine (`rm`, `mv`, `chmod`, `git`): give a one-line command;
  call scripts as `bash file.sh`. `Makefile` is protected: edit `makefile.mk`.
- Computer off: keep working in the copy, deliver an archive, write when it is back.
- After writing a file just copied to outputs the old version may arrive: `sync`, wait,
  write, stage back, `cmp`.

## Rules
- Commit messages: no attribution lines (`Co-Authored-By`, `Claude-Session`, "generated
  with"). The user's rule, it overrides any default.
- `TODO.md`: add found problems at once; done → `[x]` + "Done:" line; unfinished → top.
- Go change → `make coverage-docs` (README header + `docs/development.md`). In a sandbox
  (no rsync): `go test -coverprofile=… ./...` then `bash scripts/coverage.sh <report>`.
  E2E check count in the README header: by hand.
- New kind of test → a stand in `scripts/stands/<name>/` + a row below.
- Changed pictures: retake, do not compare by eye. `tools-*.png` come from stand `tools`. PNG bytes differ after transfer:
  compare pixels.
- Go version: `toolchain` in `go.mod`, `GO_IMAGE` in `Dockerfile`, `docs/development.md`
  change together. Analysers built with older Go cannot read 1.27 code:
  `GOTOOLCHAIN=go1.27.4 go install …`; `govulncheck` still fails.

## Sandbox
- Docker Hub 429: `docker pull mirror.gcr.io/library/<img>` (or
  `public.ecr.aws/docker/library/<img>`) + `docker tag`.
- `docker build` behind the TLS proxy: `--build-arg GO_IMAGE= NODE_IMAGE= BASE_IMAGE=`
  with images holding the proxy CA, or `PROXIMITY_IMAGE=<ready>`; apt sources → https.
- Docker daemon gone after a pause: `rm -f /var/run/docker.pid`, start `dockerd`.
- No NTP → OpenVPN waits: `STAND_OFFLINE=1` (pair stands too: without it "server starts" fails) (time server with aliases
  `time.cloudflare.com`, `time.google.com`, `pool.ntp.org`); screenshots:
  `stand_network_up` + `SCREENSHOT_NETWORK=proximity-stand`.
- No Internet → outbounds `failed` (probe `1.1.1.1:443`): give the far side `1.1.1.1`.
- Inside the container: no `curl`/`wget`/`sysctl`; there are `ip`, `iptables`, `ping`,
  `openvpn`, `awg`. Call the API from outside.
- mihomo binary for quick probes: `/home/claude/work/lab/dns/mihomo` (v1.19.34).

## Product traps
- New outbound → `POST …/stop` says *does not exist*: retry for a few seconds.
- Outbound stopped by `…/stop` stays stopped after a container restart.
- One instance as OpenVPN client and server of itself: packets dropped → two instances.
- Inbound restart deletes `inbound-<N>`: derive keys from saved secrets (`tokenKey`).
- OpenVPN logs the same line for password and token login: check `codes.json`.
- `ip rule … ipproto 1` without `to` at a used priority: *File exists* → other priority (5166).
- Rule with `ipproto … dport 53` needs `from`, else `rp_filter=1` drops DNS answers (5155).
- `tun.disable-icmp-forwarding: true`: gVisor answers every ping itself; not a fix.
- SOCKS5 domain → two DNS queries (router + far side): tell apart by server.
- `proxy-server-nameserver-policy` without `proxy-server-nameserver`: parse error; unused.
- mihomo `GET /rules`: `index`, `type`, `payload`, `extra.hitCount`; counters reset on
  every config reload. Log line: `[TCP] src --> host:port match Type(payload) using X`.
- mihomo answers HTTP `CONNECT` and SOCKS5 CONNECT at once, dials afterwards: the result is
  only in the log line (by source port) or `/connections`. A mihomo exit retries a refused
  dial for ~3 s before it closes (Tools wait 5 s).
- mihomo `/logs` stream may lag after `PUT /configs`: do not rely on the first line.
- Probe a single outbound: listener `proximity-probe` on 127.0.0.1, user `probeN`
  (`router.Describe(...).Probe`), `IN-USER` rule; test it with
  `curl -x http://probeN:<secret>@127.0.0.1:<port>`.
- mihomo DNS facts (1.19.34): `nameserver-policy` key `rule-set:<inline classical set>`
  works for KEYWORD/WILDCARD/REGEX; `hosts` gives the address of a named upstream; all
  servers of a policy are asked at once, the first answer wins (a slow DoT/DoH loses);
  through a proxy the server name is sent to the proxy; DoH uses GET `?dns=`;
  `SSL_CERT_FILE` is trusted; `fake-ip-filter-mode: rule` accepts domain rules only.
- A TUN for OpenVPN inbounds only hijacks `198.18.0.2:53`; capture hijacks `any:53`.
- DNS debug: `GET /api/router/config` (`dns.nameserver-policy`), `resolvers` in
  `GET /api/outbounds`, real query `GET /dns/query?name=&type=A` (`Mihomo.Resolve`).

## Test traps
- Capture test vs a leftover `proximity_tun`: own device name (`proximity_missing`).
- One-time code test: clock a few seconds before the next 30 s window.
- Expected OpenVPN config / `encoding/json` errors (jsonv2 in Go 1.27): build the
  expectation with the same helper (`certificateFingerprint`, `unmarshalError`).
- Unit test with a server name asks real DNS: `Options.Resolve` (`sampleResolve`).
- UDP test server on `lo` addresses: bind every address, not `0.0.0.0`.
- `docker logs` writes to stderr too: `docker()` in `scripts/stands/lib.mjs`.
- `docker restart` changes a `127.0.0.1::8899` port: `docker port` again / `signIn`;
  browser tests: fixed port.
- puppeteer: root → `--no-sandbox`, `--no-proxy-server`; sign in by `fetch('/api/login')`;
  inputs: compare `input.value`; dropdowns open on `hover`; stable dialog pictures: hide
  `#app`, blur, shoot until two equal.
- AmneziaWG server: `docker exec -d <c> amneziawg-go --foreground awg0`, wait for
  `/var/run/amneziawg/awg0.sock`, `awg genkey|pubkey`, `awg setconf`; same Jc…H4 on both.

## Stands
`stand_instance <name> [docker options] <image>`: options go before the image, after it
they are arguments of Proximity (*flag provided but not defined*).
`scripts/stands/lib.sh`, `lib.mjs`. `PROXIMITY_IMAGE` skips build, `STAND_KEEP=1` keeps
it, `STAND_OFFLINE=1` adds a time server. Checks print `PASS`/`FAIL`, exit 0 if all pass.

| Stand | Start | Checks |
|---|---|---|
| screenshots | `make screenshots`, `make screenshot <name>`, `ONLY=static`, `SCREENSHOT_TRAFFIC=30` | two linked instances, all pictures |
| pair | `bash scripts/stands/pair/stand.sh up/down/status` | `stand-a` client, `stand-b` server (`1.1.1.1:443`) |
| pair: security | `make stand-security` (1 min, 16) | TOTP single use, old cert, client ping |
| pair: tunnel | `make stand-tunnel` (20 s, 24) | pushed routes, *Receive routes*, token after restart |
| reload | `make stand-reload` (25 s, 10, browser) | page reload after `docker restart`, port `18899` |
| logs | `make stand-logs` (40 s, 21, browser) | log clearing on Start/Restart, **Clear** buttons |
| capture | `make stand-capture` (45 s, 29) | host namespace, `rp_filter=1`, SOCKS5/OpenVPN/AWG exits, counting "Internet" |
| dns | `make stand-dns` (1.5 min, 36, openssl) | per-outbound DNS, loop pins, fake-IP, DoT/DoH (own CA via `SSL_CERT_FILE`), HTTP proxy `proxy.py`, keyword/regex, server names → hosts |
| broken-config | `make stand-config` (5 s, 17) | damaged `config.json` |
| hits | `make stand-hits` (30 s, 13, browser) | route hit counters from mihomo `/rules`, reload, restart, Routes chart |
| tools | `make stand-tools` (40 s, 23, browser) | Tools page: lookup, DNS through outbounds, ping; pictures `tools-*.png` |

## Faster
- One package/test: `go test ./internal/link -run TestX`; full `make test` once at the end.
- UI only: `make dev`. Image cache: `web/` change skips Go helpers, `internal/` skips `npm ci`.
- Seed and read through the API (`docs/api.md`); browser only for pictures.
