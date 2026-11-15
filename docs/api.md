# HTTP API

The web UI talks to Proximity through a small JSON API on the same port (8899). You can
use it for scripting: provisioning endpoints, toggling filters, exporting backups.

- [Conventions](#conventions)
- [Authentication](#authentication)
- [Endpoints reference](#endpoints-reference)
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

## Endpoints reference

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
| `GET /api/dashboard` | 0* | Counts, totals, router state, endpoints with status and traffic, fallbacks, unreachable routes, warnings |
| `GET /api/history?since=<unix>` | 0* | Throughput samples newer than `since` (5 s step, up to 1 hour) |
| `DELETE /api/refused` | 2 | Clear the unreachable routes list |

\* Level 0 only when *Anonymous users can view the dashboard* is on; otherwise 1.

### Endpoints

| Method and path | Level | Description |
|---|---|---|
| `GET /api/endpoints` | 1 | List with status and traffic (secrets removed for level 1) |
| `POST /api/endpoints` | 2 | Add an endpoint |
| `PUT /api/endpoints/{id}` | 2 | Replace an endpoint (rename by changing `id`) |
| `DELETE /api/endpoints/{id}` | 2 | Delete |
| `POST /api/endpoints/{id}/move` | 2 | `{delta: -1 \| 1}` — move up or down |
| `POST /api/endpoints/{id}/restart` | 2 | Reconnect |
| `GET /api/endpoints/{id}/log` | 2 | Connection log (last 300 lines) |
| `POST /api/import/amneziawg` | 2 | `{text: "vpn://…"}` → `{config: "<.conf text>"}` |
| `GET /api/interfaces` | 1 | Host network interfaces (for HOST endpoints) |
| `GET /api/targets` | 1 | Endpoints and fallbacks usable as filter targets |

Endpoint object (fields depend on `type`):

```json
{
  "id": "SOCKS5 1",
  "name": "Amsterdam",
  "group": "Proxies",
  "type": "socks5",
  "enabled": true,
  "statistics": true,
  "socks5": {"server": "proxy.ams.example.net", "port": 1080,
             "username": "alice", "password": "secret",
             "tls": false, "skip_cert_verify": false, "udp": true}
}
```

| `type` | Options object |
|---|---|
| `host` | `host: {mode: "default" \| "interface", interface, gateway, attempts, reconnect_timeout}` |
| `socks5` | `socks5: {server, port, username, password, tls, skip_cert_verify, udp}` |
| `http` | `http: {server, port, single, https_server, https_port, ftp_server, ftp_port, username, password, tls, skip_cert_verify}` |
| `openvpn` | `openvpn: {config, username, password, otp_key, otp_mode: "challenge" \| "append", key_password, use_dns}` |
| `amneziawg` | `amneziawg: {config, use_dns}` |

Tunnels also take `monitoring: {enabled, interval, timeout, retries, reconnect_delay,
reconnect_attempts, target}`.

### Fallbacks

| Method and path | Level | Description |
|---|---|---|
| `GET /api/fallbacks` | 1 | List |
| `POST /api/fallbacks` | 2 | `{id, enabled, endpoints: [...], health_check, url, interval}` |
| `PUT /api/fallbacks/{id}` | 2 | Replace |
| `DELETE /api/fallbacks/{id}` | 2 | Delete |
| `POST /api/fallbacks/{id}/move` | 2 | `{delta}` |

### Filters

| Method and path | Level | Description |
|---|---|---|
| `GET /api/filters` | 1 | `{filters, directives, default_target, warnings, failure}` |
| `POST /api/filters` | 2 | `{id, directive, target, params: [...], enabled}` |
| `POST /api/filters/preview` | 1 | Same body → `{params, lines}` (normalized parameters and mihomo rules) or an error with the line number |
| `PUT /api/filters/default` | 2 | `{target}` — target for everything else |
| `PUT /api/filters/{key}` | 2 | Replace (filters are addressed by their internal `key`) |
| `DELETE /api/filters/{key}` | 2 | Delete |
| `POST /api/filters/{key}/move` | 2 | `{delta}` |
| `GET /api/router/config` | 2 | `{config}` — current mihomo configuration |

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
  "check_interval": 30, "capture": true
}
```

## Examples

```bash
P=http://localhost:8899
H='X-Requested-With: cli'

# sign in and keep the cookie
curl -s -c jar -H "$H" -d '{"username":"admin","password":"…"}' $P/api/login

# list endpoints with their state
curl -s -b jar $P/api/endpoints | jq -r '.endpoints[] | "\(.id)\t\(.status.state)"'

# add a SOCKS5 endpoint
curl -s -b jar -H "$H" -X POST $P/api/endpoints -d '{
  "id": "SOCKS5 3", "type": "socks5", "enabled": true, "statistics": true,
  "socks5": {"server": "10.0.0.5", "port": 1080, "udp": true}}'

# check how a filter will be normalized
curl -s -b jar -H "$H" $P/api/filters/preview -d '{
  "directive": "IP-CIDR", "target": "DIRECT", "params": ["1.2.3.4:443", "2001:db8::1"]}'
# {"lines":["AND,((IP-CIDR,1.2.3.4/32),(DST-PORT,443)),DIRECT","IP-CIDR6,2001:db8::1/128,DIRECT"],
#  "params":["1.2.3.4/32:443","2001:db8::1/128"]}

# nightly backup
curl -s -b jar $P/api/backup > proximity-$(date +%F).json
```
