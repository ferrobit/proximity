// Checks of the stand "dns": which DNS server is asked for a name and from which address.
// The DNS server of the stand answers on 1.1.1.1 (the resolver of the system) and on 1.1.1.53
// (the server given to the outbounds) and prints every query. A route IP-CIDR stands first,
// so the router has to resolve the domains of the routes below it by itself.
import { check, docker, finish, logOf, signIn, sleep, stateOf, waitFor } from '../lib.mjs'

const [host, exit, dns] = process.argv.slice(2)
const system = '1.1.1.1'
const custom = '1.1.1.53'
const webProxy = '1.1.1.80'
const addressOf = (container) => docker('inspect', '-f', '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}', container).trim()
const inHost = (...command) => docker('exec', host, ...command)
const queries = (name) => docker('logs', dns).split('\n').filter((line) => line.split(' ')[1] === name)
const dropped = () => Number((inHost('awk', '/^TcpExt:/ { if (++n == 1) { for (i = 1; i <= NF; i++) if ($i == "IPReversePathFilter") c = i } else print $c }',
  '/proc/net/netstat').trim()) || 0)

// NOTE: The connection only makes the router resolve the name: the TUN accepts it at once.
async function asked(name) {
  inHost('bash', '-c', `timeout 5 bash -c 'exec 3<>/dev/tcp/${name}/80'`)
  const found = await waitFor(() => (queries(name).length ? queries(name) : null), 10)
  console.log(`${name}: ${(found || ['no query']).join(' | ')}`)
  return found || []
}

const only = (lines, from, to, transport) => lines.length > 0 && lines.every((line) =>
  line.includes(`from=${from} `) && line.includes(`to=${to} `) && line.endsWith(transport))
// NOTE: A SOCKS5 proxy gets the domain itself, so the exit asks its own resolver (1.1.1.1) too;
// the query of the router is the one to the server of the outbound, and nothing may come
// from the address of the host.
const through = (lines, from, transport, host) => lines.some((line) => line.includes(`from=${from} to=1.1.1.53 `) && line.endsWith(transport)) &&
  !lines.some((line) => line.includes(`from=${host} `))

async function settle(client, change) {
  const current = await client.call('GET', '/api/settings')
  await client.call('PUT', '/api/settings', { ...current, ...change })
  await sleep(3000)
}

async function run() {
  const [here, far] = [addressOf(host), addressOf(exit)]
  const client = await signIn(host)
  await settle(client, { capture: true, probe_target: '1.1.1.2:80' })
  const remote = await signIn(exit)
  const inbound = { enabled: true, autostart: true, statistics: true, outbound: '@host' }
  await remote.call('POST', '/api/inbounds', { ...inbound, id: 'Exit', type: 'socks5', port: 7800, anonymous: true, users: [],
    socks5: { listen: '0.0.0.0', udp: true } })
  await remote.call('POST', '/api/inbounds', { ...inbound, id: 'ExitTCP', type: 'socks5', port: 7802, anonymous: true, users: [],
    socks5: { listen: '0.0.0.0', udp: false } })
  await remote.call('POST', '/api/inbounds', { ...inbound, id: 'VPN', type: 'openvpn', port: 7801, anonymous: false,
    users: [{ enabled: true, name: 'alice', password: 'wonderland' }], openvpn: { server: far, protocol: 'udp', otp: false, dns: [custom] } })
  check('exit inbounds start', Boolean(await waitFor(async () => (await stateOf(remote, 'inbounds', 'Exit')).state === 'up' &&
    (await stateOf(remote, 'inbounds', 'ExitTCP')).state === 'up' && (await stateOf(remote, 'inbounds', 'VPN')).state === 'up', 90)))
  const config = (await remote.call('GET', '/api/inbounds/VPN/config?user=alice')).config.replace(`remote ${far} `, 'remote vpn.stand.test ')
  const outbound = { enabled: true, autostart: true, statistics: true, attempts: 3, reconnect_timeout: 3 }
  await client.call('POST', '/api/outbounds', { ...outbound, id: 'Proxy', type: 'socks5', socks5: { server: 'exit.stand.test', port: 7800, udp: true },
    dns: { servers: [custom], exit: '' } })
  await client.call('POST', '/api/outbounds', { ...outbound, id: 'ProxyTCP', type: 'socks5', socks5: { server: far, port: 7802, udp: false },
    dns: { servers: [custom], exit: '' } })
  await client.call('POST', '/api/outbounds', { ...outbound, id: 'Plain', type: 'socks5', socks5: { server: far, port: 7800, udp: true },
    dns: { servers: [custom], exit: '@host' } })
  await client.call('POST', '/api/outbounds', { ...outbound, id: 'Secure', type: 'socks5', socks5: { server: far, port: 7800, udp: true },
    dns: { servers: [`tls://${custom}`], exit: '' } })
  await client.call('POST', '/api/outbounds', { ...outbound, id: 'Web', type: 'http', http: { server: webProxy, port: 3128, single: true },
    dns: { servers: ['https://doh.stand.test/dns-query'], exit: '' } })
  for (const [directive, target, ...params] of [['IP-CIDR', 'DIRECT', '10.99.0.0/16'], ['DOMAIN-SUFFIX', 'DIRECT', 'direct.test'],
    ['DOMAIN-SUFFIX', 'Proxy', 'viaproxy.test', 'stand.test'], ['DOMAIN-SUFFIX', 'ProxyTCP', 'viatcp.test'],
    ['DOMAIN-SUFFIX', 'Plain', 'viahost.test'], ['DOMAIN-SUFFIX', 'Secure', 'viadot.test'], ['DOMAIN-SUFFIX', 'Web', 'viadoh.test'],
    ['DOMAIN-KEYWORD', 'Proxy', 'kwproxy'], ['DOMAIN-REGEX', 'Proxy', '^rx[0-9]+\\.example$']]) {
    await client.call('POST', '/api/routes', { directive, target, enabled: true, params })
  }
  check('capture rules are installed, the DNS rule has a source', Boolean(await waitFor(() =>
    /5155:\s+from 0\.0\.0\.0 iif lo ipproto udp dport 53 lookup 51000/.test(inHost('ip', 'rule')) && inHost('ip', 'rule').includes('5170'), 60)))
  const before = dropped()

  // The names of the configuration.
  check('proxy outbound with a server name connects', Boolean(await waitFor(async () => (await stateOf(client, 'outbounds', 'Proxy')).state === 'up', 60)),
    (await stateOf(client, 'outbounds', 'Proxy')).message)
  await sleep(3000)
  await client.call('POST', '/api/outbounds', { ...outbound, id: 'Tunnel', type: 'openvpn',
    openvpn: { config, username: 'alice', password: 'wonderland', use_dns: true, use_routes: false },
    dns: { servers: ['tls://dot.stand.test'], exit: '' },
    monitoring: { enabled: false, interval: 30, timeout: 5, retries: 3, target: '1.1.1.2:80' } })
  await client.call('POST', '/api/routes', { directive: 'DOMAIN-SUFFIX', target: 'Tunnel', enabled: true, params: ['corp.test'] })
  check('tunnel outbound with a remote name connects', Boolean(await waitFor(async () => (await stateOf(client, 'outbounds', 'Tunnel')).state === 'up', 90)),
    (await stateOf(client, 'outbounds', 'Tunnel')).message)
  check('remote of the tunnel is replaced by its real address', (await logOf(client, 'outbounds', 'Tunnel')).includes(`'vpn.stand.test' is resolved to ${far}`))
  await client.call('POST', '/api/outbounds/Proxy/restart')
  check('proxy outbound connects again when its own domain is routed to it', Boolean(await waitFor(async () =>
    (await stateOf(client, 'outbounds', 'Proxy')).state === 'up' && queries('exit.stand.test').length > 1, 60)))
  console.log(`names of the configuration: ${queries('exit.stand.test').concat(queries('vpn.stand.test')).join(' | ')}`)
  check('server name of the proxy is asked from the system resolver, never through the proxy', only(queries('exit.stand.test'), here, system, 'udp'))
  check('remote name of the tunnel follows the DNS of its route (the proxy)', only(queries('vpn.stand.test'), far, custom, 'udp'))
  const generated = JSON.parse((await client.call('GET', '/api/router/config')).config)
  check('server name of the proxy is pinned out of the DNS of the proxy', JSON.stringify(generated.dns['nameserver-policy']['exit.stand.test']) === '["system"]' &&
    JSON.stringify(generated.dns['nameserver-policy']['+.stand.test']) === `["${custom}#Proxy"]`)
  await client.call('POST', '/api/time/check')
  const clock = await client.call('GET', '/api/time')
  const servers = ['time.cloudflare.com', 'time.google.com', 'pool.ntp.org'].flatMap(queries).filter((line) => line.includes(`from=${here} `))
  check('time servers are resolved by the router, for real', ['host', 'corrected'].includes(clock.state) && only(servers, here, system, 'udp'),
    `${clock.state}, ${servers.length} queries`)
  const placeholder = inHost('getent', 'ahostsv4', 'app.direct.test').split('\n')[0]
  check('application of the host gets a placeholder address', placeholder.startsWith('198.18.'), placeholder)

  // The domains of the routes.
  check('domain of a DIRECT route is asked from the system resolver, directly', only(await asked('a.direct.test'), here, system, 'udp'))
  check('domain of a proxy route is asked from the server of the proxy, through the proxy', through(await asked('a.viaproxy.test'), far, 'udp', here))
  check('proxy without UDP gets the query over TCP', through(await asked('a.viatcp.test'), far, 'tcp', here))
  const tunneled = await asked('a.corp.test')
  check('domain of a tunnel route is asked at the far side of the tunnel', tunneled.length > 0 && tunneled.every((line) => line.includes(`from=${far} `)))
  const page = inHost('bash', '-c', "timeout 8 bash -c 'exec 3<>/dev/tcp/b.corp.test/80; printf \"GET /api/session HTTP/1.0\\r\\n\\r\\n\" >&3; head -c 12 <&3'")
  check('HTTP by name through the tunnel gets an answer', page.startsWith('HTTP/1.0 200'), page.trim())
  const direct = await asked('a.viahost.test')
  check('another DNS exit: the server of the outbound is asked directly', direct.some((line) => line.includes(`from=${here} to=${custom} udp`)) &&
    !direct.some((line) => line.includes(`from=${here} to=${system} `)))
  const views = (await client.call('GET', '/api/outbounds')).outbounds
  const resolvers = Object.fromEntries(views.map((view) => [view.id, view.resolvers.join(' ')]))
  check('API shows the DNS servers of the outbounds', resolvers.Proxy === `${custom}#Proxy` && resolvers.ProxyTCP === `tcp://${custom}#ProxyTCP` &&
    resolvers.Plain === custom && resolvers.Tunnel === `${custom}#Tunnel tls://dot.stand.test#Tunnel`, JSON.stringify(resolvers))

  // Names of DNS servers, DoT and DoH, keyword and regex routes, the health check of a fallback.
  // NOTE: mihomo asks all servers of a domain at once and drops the slower ones: the outbounds
  // with DoT and DoH have only them, the tunnel is checked with the tools of the page.
  const hosts = await waitFor(async () => {
    const value = JSON.parse((await client.call('GET', '/api/router/config')).config).hosts || {}
    return value['dot.stand.test'] ? value : null
  }, 30)
  check('name of a DNS server of the tunnel is resolved into the hosts of the router', JSON.stringify(hosts?.['dot.stand.test']) === `["${custom}"]`,
    JSON.stringify(hosts))
  check('name of the DNS server is asked through its tunnel', queries('dot.stand.test').some((line) => line.includes(`from=${far} to=${custom} `)),
    queries('dot.stand.test').join(' | '))
  const lookup = await client.call('POST', '/api/tools/lookup', { host: 'a.corp.test', port: 80 })
  const secure = (lookup.outbounds.find((item) => item.id === 'Tunnel')?.resolvers || []).find((item) => item.server === 'tls://dot.stand.test#Tunnel')
  check('DoT server by name answers through the tunnel', secure && !secure.error && secure.addresses.length > 0, JSON.stringify(secure))
  check('a name through the tunnel to a Proximity inbound gets a real address', JSON.stringify(lookup.addresses) === '["1.1.1.2"]',
    JSON.stringify(lookup.addresses))
  const exitTun = JSON.parse((await remote.call('GET', '/api/router/config')).config).tun || {}
  check('an inbound without capture takes only the queries to its resolver', JSON.stringify(exitTun['dns-hijack']) === '["198.18.0.2:53"]',
    JSON.stringify(exitTun['dns-hijack']))
  check('DoT server is asked through the SOCKS5 proxy', (await asked('a.viadot.test')).some((line) => line.includes(`from=${far} `) && line.endsWith('tls')))
  check('DoH server is asked through the HTTP proxy', (await asked('a.viadoh.test')).some((line) => line.endsWith('https')) &&
    docker('logs', dns).split('\n').some((line) => line.startsWith('CONNECT doh.stand.test:443 ') && line.endsWith(`from=${here}`)))
  check('keyword route asks the DNS of its outbound', through(await asked('a.kwproxy.example'), far, 'udp', here))
  check('regex route asks the DNS of its outbound', through(await asked('rx42.example'), far, 'udp', here))
  await client.call('POST', '/api/fallbacks', { id: 'Checked', enabled: true, health_check: true, url: 'http://health.stand.test/',
    interval: 60, outbounds: ['Tunnel'] })
  const health = await waitFor(() => (queries('health.stand.test').length ? queries('health.stand.test') : null), 30)
  check('health check of a fallback resolves its name through the tunnel', Boolean(health) && health.every((line) => line.includes(`from=${far} `)),
    (health || ['no query']).join(' | '))
  const named = Object.fromEntries((await client.call('GET', '/api/outbounds')).outbounds.map((view) => [view.id, view.resolvers.join(' ')]))
  check('API shows DoT and DoH servers with their exits', named.Secure === `tls://${custom}#Secure` &&
    named.Web === 'https://doh.stand.test/dns-query#Web' && named.Tunnel === `${custom}#Tunnel tls://dot.stand.test#Tunnel`, JSON.stringify(named))

  // The default servers of Settings.
  check('domain without a route is asked from the system resolver', only(await asked('a.other.test'), here, system, 'udp'))
  await settle(client, { dns: { servers: [custom], exit: 'Proxy' } })
  check('default DNS servers of Settings are asked through their exit', through(await asked('b.other.test'), far, 'udp', here))
  check('DIRECT route follows the default servers too', through(await asked('b.direct.test'), far, 'udp', here))
  await settle(client, { dns: { servers: [], exit: '@host' }, default_target: 'Proxy' })
  check('domain without a route follows the DNS of the default target', through(await asked('c.other.test'), far, 'udp', here))
  check('DIRECT route keeps the system resolver with another default target', only(await asked('c.direct.test'), here, system, 'udp'))
  check('answers to the router pass the strict reverse path filter', dropped() === before, `dropped ${dropped() - before}`)
  const warnings = (await client.call('GET', '/api/routes')).problems || []
  check('router configuration has no warnings', warnings.length === 0, warnings.join('; '))
}

run().then(finish).catch((error) => {
  console.error(`FAIL  ${error.message}`)
  process.exit(1)
})
