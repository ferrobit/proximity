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

## Stands

A stand is a ready environment for a kind of test: started with one command, removed at
the end. Do not build a test environment by hand twice: when a new kind of test appears,
make a stand for it in `scripts/stands/<stand>/` and add its line here.

| Stand | Start | What it gives |
|---|---|---|
| screenshots | `make screenshots`, `make screenshot <picture>` | Two linked instances in their own Docker network: SOCKS5 and HTTP proxies, an OpenVPN tunnel, routes, inbounds, users, generated traffic, no Internet needed (`scripts/screenshots.sh`, `scripts/screenshots/`) |

More stands are planned, see *Test stands* in `TODO.md`.

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
