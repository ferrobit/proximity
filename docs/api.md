# HTTP API

The web UI talks to Proximity through a small JSON API on the same port (8899). You can
use it for scripting: provisioning outbounds, toggling routes, exporting backups.

- [Conventions](#conventions)
- [Authentication](#authentication)
- [API endpoints reference](#api-endpoints-reference)
- [Examples](#examples)

## Conventions

- Request and response bodies are JSON (`Content-Type: application/json`).
- **Every request other than `GET` must have the header `X-Requested-With`** (any value).
  Without it the server answers `403 request header does not exist`. This protects against
  cross-site requests.
- Errors: HTTP status `4xx` and `{"error": "message"}`.
- IDs in paths are URL-encoded: `OpenVPN 1` → `OpenVPN%201`.
- Each response carries `X-Proximity-Time` — server processing time in milliseconds.

## Authentication

Sign in with `POST /api/login`; the server sets the `proximity_session` cookie (HTTP-only,
7 days). Send the cookie with later requests.

Access levels:

| Level | Role |
|---|---|
| 0 | anyone (anonymous) |
| 1 | User |
| 2 | Operator |
| 3 | Administrator |

A request below the required level gets `401` (not signed in) or `403` (role too low).

## API endpoints reference

> [!NOTE]
> Earlier releases called the resources `endpoints` and `filters`. Configuration
> files and backups with these keys are still read and converted on load.

### Session and accounts

| Method and path | Level | Body / query | Description |
|---|---|---|---|
| `GET /api/session` | 0 | — | Current user, role, version, `setup` (no users yet), `host_network` |
| `POST /api/setup` | 0 | `{username, password, confirm}` | Create the first administrator (only when no users exist) |
| `POST /api/login` | 0 | `{username, password}` | Sign in, sets the cookie |
| `POST /api/logout` | 0 | — | Sign out |
| `POST /api/password` | 1 | `{current, password, confirm}` | Change own password |
| `GET /api/users` | 3 | — | List users |
| `POST /api/users` | 3 | `{name, role, password, confirm}` | Add a user (`administrator`, `operator`, `user`) |
| `PUT /api/users/{name}` | 3 | `{role, password, confirm}` | Change role and/or password (empty password keeps it) |
| `DELETE /api/users/{name}` | 3 | — | Delete a user |

### Dashboard

| Method and path | Level | Description |
|---|---|---|
| `GET /api/dashboard` | 0* | Counts, totals, router state, outbounds with status and traffic, fallbacks, unreachable routes, warnings |
| `GET /api/history?since=<unix>` | 0* | Throughput and connection count samples newer than `since` (5 s step, up to 1 hour) |
| `DELETE /api/refused` | 2 | Clear the unreachable routes list |

\* Level 0 only when *Anonymous users can view the dashboard* is on; otherwise 1.

### Outbounds

| Method and path | Level | Description |
|---|---|---|
| `GET /api/outbounds` | 1 | List with status and traffic (secrets removed for level 1) |
| `POST /api/outbounds` | 2 | Add an outbound |
| `PUT /api/outbounds/{id}` | 2 | Replace an outbound (rename by changing `id`) |
| `DELETE /api/outbounds/{id}` | 2 | Delete |
| `POST /api/outbounds/{id}/move` | 2 | `{delta: -1 \| 1}` — move up or down |
| `POST /api/outbounds/{id}/start` | 2 | Start an outbound |
| `POST /api/outbounds/{id}/stop` | 2 | Stop an outbound; it stays stopped after restarts |
| `POST /api/outbounds/{id}/restart` | 2 | Reconnect; starts a stopped or failed outbound |
| `GET /api/outbounds/{id}/log` | 2 | Connection log (last `log_lines` lines, `verbose_log_lines` when `verbose` is on; kept across restarts) |
| `POST /api/import/amneziawg` | 2 | `{text: "vpn://…"}` → `{config: "<.conf text>"}` |
| `GET /api/interfaces` | 1 | Host network interfaces (for HOST outbounds) |
| `GET /api/targets` | 1 | Outbounds and fallbacks usable as route targets |

Outbound object (fields depend on `type`):

```json
{
  "id": "SOCKS5 1",
  "type": "socks5",
  "enabled": true,
  "autostart": true,
  "attempts": 3,
  "reconnect_timeout": 10,
  "statistics": true,
  "verbose": false,
  "socks5": {"server": "proxy.ams.example.net", "port": 1080,
             "username": "alice", "password": "secret",
             "tls": false, "skip_cert_verify": false, "udp": true}
}
```

| `type` | Options object |
|---|---|
| `host` | `host: {mode: "default" \| "interface", interface, gateway}` |
| `socks5` | `socks5: {server, port, username, password, tls, skip_cert_verify, udp}` |
| `http` | `http: {server, port, single, https_server, https_port, ftp_server, ftp_port, username, password, tls, skip_cert_verify}` |
| `openvpn` | `openvpn: {config, username, password, otp_key, otp_mode: "challenge" \| "append", key_password, use_dns}` |
| `amneziawg` | `amneziawg: {config, use_dns}` |

Tunnels also take `monitoring: {enabled, interval, timeout, retries, target}`.

Common fields of outbounds and inbounds: `autostart` (default `true` when omitted) — start
when the router starts; `attempts` (1…100, default 3) — failed attempts in a row before
the state becomes `failed`; `reconnect_timeout` (1…3600 s, default 10) — pause between
attempts. A failed tunnel or inbound waits for **Start** / **Restart**; a failed HOST or
proxy outbound keeps being checked every *Check interval* and recovers by itself.
Configurations before version 7 move `host.attempts` / `host.reconnect_timeout` and
`monitoring.reconnect_attempts + 1` / `monitoring.reconnect_delay` here.

Services are all outbounds and inbounds. Their state (`running`, `stopped` or `failed`) is
saved to `services.json` in the settings folder, and their logs to `logs/`. When the router
starts (with the container or by **Start**), a service is started only if `autostart` is
on and it was running before; a stopped or failed service keeps its state and message.
A failed service is started again when its settings change. Status `stopped` means
stopped by a user, by the router stop, or not started because `autostart` is off (see
`message`).

### Inbounds

| Method and path | Level | Description |
|---|---|---|
| `GET /api/inbounds` | 1 | `{inbounds, ports: [7800, 7898]}`; every inbound carries `status` (`state`, `message`, `device`, `address`, `clients`) and `traffic`. Level 1 gets passwords and private keys removed (`redacted: true`) |
| `POST /api/inbounds` | 2 | Add (see below); empty `id` → `SOCKS5 <N>` / `OpenVPN <N>`, `port: 0` → the first free port. Returns the stored inbound with generated keys |
| `PUT /api/inbounds/{id}` | 2 | Replace; `type` cannot change, `id` may |
| `DELETE /api/inbounds/{id}` | 2 | Delete |
| `POST /api/inbounds/{id}/move` | 2 | `{delta}` |
| `POST /api/inbounds/{id}/start` | 2 | Start the listener or the VPN server |
| `POST /api/inbounds/{id}/stop` | 2 | Stop it; it stays stopped after restarts |
| `POST /api/inbounds/{id}/restart` | 2 | Restart; starts a stopped or failed inbound |
| `GET /api/inbounds/{id}/log` | 2 | `{lines}` — last `log_lines` log lines (`verbose_log_lines` when `verbose` is on), kept across restarts |
| `GET /api/inbounds/{id}/config?user=&embed=&host=` | 2 | OpenVPN client configuration → `{config, file}`; `file` is `<ID>-<username>.conf`. Empty `user` = anonymous, `embed=true` writes the credentials into the file, `host` is used when the inbound has no public address |
| `POST /api/inbounds/generate` | 2 | `{kind, ca_certificate, ca_key}` → new key material. `kind`: `all`, `pki` (CA + server + client), `server`, `client` (both signed by the given CA), `tls_crypt`, `otp_key`, `tls` (self-signed certificate for SOCKS5) |

Inbound object: `{id, type, enabled, autostart, attempts, reconnect_timeout, statistics,
verbose, port, outbound, anonymous,
users: [{enabled, name, password}]}` plus one block by `type`. `outbound` is `@router`
(routes decide), `@host` (host network connection) or an outbound ID.

| Type | Block |
|---|---|
| `socks5` | `socks5: {listen, udp, tls, certificate, key}` |
| `openvpn` | `openvpn: {server, public_port, listen, protocol: "udp" \| "tcp", network, redirect_gateway, routes: [...], dns: [...], client_to_client, max_clients, keepalive_interval, keepalive_timeout, renegotiation, mtu, ciphers, digest, tls_version, verbosity, otp, otp_key, ca_certificate, ca_key, certificate, key, client_certificate, client_key, tls_crypt}` |

Empty key and certificate fields are generated on save.

### Fallbacks

| Method and path | Level | Description |
|---|---|---|
| `GET /api/fallbacks` | 1 | List |
| `POST /api/fallbacks` | 2 | `{id, enabled, outbounds: [...], health_check, url, interval}` |
| `PUT /api/fallbacks/{id}` | 2 | Replace |
| `DELETE /api/fallbacks/{id}` | 2 | Delete |
| `POST /api/fallbacks/{id}/move` | 2 | `{delta}` |

### Routes

| Method and path | Level | Description |
|---|---|---|
| `GET /api/routes` | 1 | `{routes, directives, default_target, warnings, failure}` |
| `POST /api/routes` | 2 | `{id, directive, target, params: [...], enabled}` |
| `POST /api/routes/preview` | 1 | Same body → `{params, lines}` (normalized parameters and mihomo rules) or an error with the line number |
| `PUT /api/routes/default` | 2 | `{target}` — target for everything else |
| `PUT /api/routes/{key}` | 2 | Replace (routes are addressed by their internal `key`) |
| `DELETE /api/routes/{key}` | 2 | Delete |
| `POST /api/routes/{key}/move` | 2 | `{delta}` |
| `GET /api/router/config` | 2 | `{config}` — current mihomo configuration |
| `POST /api/router/start` | 2 | Start the router (mihomo, inbounds, host traffic capture) |
| `POST /api/router/stop` | 2 | Stop the router |
| `POST /api/router/restart` | 2 | Restart the mihomo process; starts a stopped router |

Router actions wait until the change is applied (up to 30 s) and return
`{router, failure, completed}`: `router` is the router state from the dashboard,
`failure` is the error of the last configuration apply, `completed` is `false` when the
wait timed out. The requested state is saved to `router.json` in the settings folder.
The dashboard `router` object has `running` and `stopped` (stopped on request, not by a
failure).

### Settings and backup

| Method and path | Level | Description |
|---|---|---|
| `GET /api/settings` | 1 | Settings |
| `PUT /api/settings` | 3 | Replace settings |
| `GET /api/backup` | 2 | Export the configuration (without users) |
| `POST /api/backup` | 2 | Import; an Operator's import keeps the current settings |

Settings object:

```json
{
  "mixed_port": 7899, "bind_address": "*", "default_target": "DIRECT",
  "log_level": "warning", "ipv6": false, "anonymous_dashboard": true,
  "connectivity_checks": true, "probe_target": "1.1.1.1:443",
  "check_interval": 30, "capture": true,
  "unreachable_limit": 100, "unreachable_lifetime": 86400, "autostart": true,
  "log_lines": 300, "verbose_log_lines": 3000
}
```

`autostart` — start the router when the container starts, but only if it was running
when the container stopped. When it is `false`, the router always stays stopped after a
restart. Configurations and backups older than version 5 get `true`.

`unreachable_limit` (1…10000) and `unreachable_lifetime` (seconds, 60…31536000) bound the
*Unreachable routes* list; omitted or zero values mean the defaults.

`log_lines` and `verbose_log_lines` (10…100000) — lines kept in the log of an outbound or
inbound with `verbose` off and on; omitted or zero values mean 300 and 3000.

## Examples

```bash
P=http://localhost:8899
H='X-Requested-With: cli'

# sign in and keep the cookie
curl -s -c jar -H "$H" -d '{"username":"admin","password":"…"}' $P/api/login

# list outbounds with their state
curl -s -b jar $P/api/outbounds | jq -r '.outbounds[] | "\(.id)\t\(.status.state)"'

# add a SOCKS5 outbound
curl -s -b jar -H "$H" -X POST $P/api/outbounds -d '{
  "id": "SOCKS5 3", "type": "socks5", "enabled": true, "statistics": true,
  "socks5": {"server": "10.0.0.5", "port": 1080, "udp": true}}'

# check how a route will be normalized
curl -s -b jar -H "$H" $P/api/routes/preview -d '{
  "directive": "IP-CIDR", "target": "DIRECT", "params": ["1.2.3.4:443", "2001:db8::1"]}'
# {"lines":["AND,((IP-CIDR,1.2.3.4/32),(DST-PORT,443)),DIRECT","IP-CIDR6,2001:db8::1/128,DIRECT"],
#  "params":["1.2.3.4/32:443","2001:db8::1/128"]}

# add an OpenVPN inbound for two users and save bob's client configuration
curl -s -b jar -H "$H" -X POST $P/api/inbounds -d '{
  "type": "openvpn", "enabled": true, "statistics": true, "outbound": "@router",
  "users": [{"enabled": true, "name": "bob", "password": "builder"},
            {"enabled": true, "name": "eve", "password": "listener"}],
  "openvpn": {"server": "vpn.example.com", "redirect_gateway": true}}'
curl -s -b jar "$P/api/inbounds/OpenVPN%201/config?user=bob" | jq -r .config > "OpenVPN 1-bob.conf"

# nightly backup
curl -s -b jar $P/api/backup > proximity-$(date +%F).json
```
