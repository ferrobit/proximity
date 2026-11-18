# Notes for AI agents

Short list of what already failed in this project and what to do instead, and how to
save time. Add a line when a new command fails; keep it short.

## Failed → use instead

| Failed | Use instead |
|---|---|
| Writing `Makefile` with remote file tools: *protected file* | Edit `makefile.mk`; `Makefile` only includes it. A change of `Makefile` itself: give the user a Git patch (`git apply`) |
| `rm`, `mv`, `chmod`, `git` on the user's machine: no shell there | Write the new file and give the user the one-line command; call a script through `bash file.sh` so it works without `chmod +x` |
| Writing a file to the user's folder right after copying it to the outputs folder: the old version arrived | `sync`, wait some seconds, write, then stage the file back and `cmp` it with the tested copy |
| Comparing PNG files by bytes: the transfer adds metadata | Compare pixels (`make screenshots` does it) |
| `docker build` in a sandbox behind a TLS proxy: certificate errors | `--build-arg GO_IMAGE=… NODE_IMAGE=… BASE_IMAGE=…` with images that hold the proxy CA, or `PROXIMITY_IMAGE=<ready image>` for the screenshots |
| `docker …`: *Cannot connect to the Docker daemon* after a pause in a sandbox | `rm -f /var/run/docker.pid`, start `dockerd` again, then repeat |
| OpenVPN waits for time synchronization: no NTP in a sandbox | A local NTP server in the Docker network with the aliases `time.cloudflare.com`, `time.google.com`, `pool.ntp.org`; for the screenshots pass the network as `SCREENSHOT_NETWORK` |
| `curl`, `wget`, `sysctl` inside the Proximity container: not installed | Call the API from outside; inside there are `ip`, `iptables`, `ping`, `openvpn`, `awg` |
| `POST /api/outbounds/{id}/stop` right after creating the outbound: *does not exist* | Retry for a few seconds: the control loop learns about new items a moment later |
| One instance as OpenVPN client and server of itself: the kernel drops the packets | Two instances in one Docker network (the screenshot stand does it) |
| Outbounds `failed` in a network without Internet: probe target `1.1.1.1:443` | Give the far side the address `1.1.1.1` and redirect port 443 (see `scripts/screenshots.sh`) |
| puppeteer: Chrome does not start as root; local pages go to the proxy | `--no-sandbox` for root, `--no-proxy-server` |
| puppeteer: waiting for the sign-in form | The dashboard is public: sign in with `fetch('/api/login')` inside the page |
| puppeteer: a table row is not found by the text of an input | Compare `input.value`, not `textContent` |
| puppeteer: `click` on a dropdown button, the menu does not open | `hover`: Ant Design dropdowns open on hover |
| puppeteer: the same dialog gives different pixels | Hide `#app` behind the dialog, blur the focused control, take the picture until two in a row are equal |
| `make test` with a running Proximity on the same machine | Stop it first: `TestManager_Capture` needs no `proximity_tun` interface |
| Node `execFileSync('docker', ['logs', …])`: the log is empty | `docker logs` writes to stderr too: read both streams (`docker()` in `scripts/stands/lib.mjs`) |
| A test with a one-time code waits up to 30 seconds for the next code | Give the test a clock that stands a few seconds before the next 30-second window (`TestManager_RejectedCode`) |
| A test expects the exact text of the generated OpenVPN server config | Build the expected text from the same helper (`certificateFingerprint`) instead of a constant |
| The user's computer is off: files cannot be written | Keep working in the copy, hand over the changed files as an archive and write them when the computer is back |
| A restart of an inbound removes its folder `inbound-<N>`: a key generated there is new every time | Derive such a key from the saved secrets of the inbound (`tokenKey`), do not generate it at start |
| `docker restart` of a stand instance: the UI port published as `127.0.0.1::8899` changes | Sign in again (`signIn`) after the restart; for a browser test publish a fixed port (stand `reload`) |
| An outbound stopped with `POST …/stop` stays stopped after a restart of the container | Start it again with `POST …/start` |
| A check counts log lines of a successful login: OpenVPN writes the same line for a password and for a token | Look at the side effect: `codes.json` is filled only by a login with a one-time code |
| `make test`, `make cover`, `make coverage-docs` in a sandbox: *rsync is required* | Run `go test -coverprofile=… ./...` in the working tree and give its output to `bash scripts/coverage.sh <report>` |
| A test expects the exact text of an `encoding/json` error: Go 1.25+ with `jsonv2` (default in the user's Go 1.27) prints other type names | Build the expected text with `unmarshalError` (the same `json.Unmarshal` call) or give the code its own message; check with `GOTOOLCHAIN=go1.27.1 go test ./...` |

## Rules

- **`TODO.md`**: a problem or an idea found during the work is added to `TODO.md` at
  once, without a separate request. A finished item is marked `[x]` with a short "Done:"
  line. When the work is interrupted, the unfinished part goes to the top of `TODO.md`.
- **Coverage**: after every change of the Go code run `make coverage-docs`. It writes the
  number of tests and the coverage into the header of `README.md` and into
  `docs/development.md`. Update the number of end-to-end checks in the `README.md` header
  by hand when a stand gets new checks.
- **Stands**: a new kind of test gets a ready stand (see below) and its line in the table.

## Stands

A stand is a ready environment for a kind of test: started with one command, removed at
the end. Do not build a test environment by hand twice: when a new kind of test appears,
make a stand for it in `scripts/stands/<stand>/` and add its line here.

| Stand | Start | What it gives |
|---|---|---|
| screenshots | `make screenshots`, `make screenshot <picture>` | Two linked instances in their own Docker network: SOCKS5 and HTTP proxies, an OpenVPN tunnel, routes, inbounds, users, generated traffic, no Internet needed (`scripts/screenshots.sh`, `scripts/screenshots/`) |
| pair | `bash scripts/stands/pair/stand.sh up` / `down` / `status` | Two empty linked instances: `proximity-stand-a` (client side) and `proximity-stand-b` (server side, answers for `1.1.1.1:443`). A check adds what it needs through the API |
| pair: security | `make stand-security` (1 minute, 16 checks) | OpenVPN inbound with TOTP and two users against two outbounds: single use of one-time codes, rejection of an old client certificate, ping of clients |
| pair: tunnel | `make stand-tunnel` (20 seconds, 24 checks) | OpenVPN inbound with pushed routes and an outbound with *Receive routes*: defaults of a new inbound (network, MTU), the provided route (first, follows the connection, moved, kept after a restart), the token after a restart of the inbound, a disabled user, the instance mark |
| reload | `make stand-reload` (20 seconds, 7 checks, needs a browser) | One instance on the fixed port `127.0.0.1:18899` and a headless browser: the open page reloads itself after `docker restart`, waits with a notice while a dialog is open |
| broken-config | `make stand-config` (5 seconds, 17 checks) | One instance started with the damaged `config.json`: what is dropped and kept, the copy of the file, the report in the API |

Common parts: `scripts/stands/lib.sh` (image, network, instance, removal) and `lib.mjs`
(API client, `docker`, `check`, `waitFor`). `PROXIMITY_IMAGE` skips the build,
`STAND_KEEP=1` leaves the stand running, `STAND_OFFLINE=1` adds a time server for a
network without Internet. A check prints `PASS` / `FAIL` lines and exits with 0 only when
all passed. More stands are planned, see *Test stands* in `TODO.md`.

## Faster

- One picture: `make screenshot <name>`; dialogs only: `make screenshots ONLY=static`
  (about 1.5 minutes, no traffic wait). All pictures wait 5 minutes for traffic;
  `SCREENSHOT_TRAFFIC=30` is enough to check a change.
- A picture that had to change is replaced without opening it and comparing by eye.
- The image is rebuilt from cache: a change in `web/` does not rebuild the Go helpers, a
  change in `internal/` does not run `npm ci`. Reuse an image with `PROXIMITY_IMAGE`.
- Go: run one package or one test (`go test ./internal/link -run TestManager_Recovery`),
  the whole `make test` and `make cover` once before delivery.
- UI only: `make dev` (Vite reloads the page), no image build.
- API first: seed data and read states through the API (`docs/api.md`), use the browser
  only for pictures.
- Deliver all changed files in one write call, then check sizes with one listing.
- `docs/development.md` describes the build, the tests and the screenshot stand; `TODO.md`
  holds the open tasks.
