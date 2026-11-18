// Checks of the stand "capture": where TCP and ICMP of a host with transparent capture leave.
// 1.1.1.1 has the route IP-CIDR -> SOCKS5 exit, 1.1.1.2 has no route (DIRECT), 1.1.1.3 has the
// route IP-CIDR -> OpenVPN tunnel. The setting "Keep ping of the host on its routes"
// (ping_filter) is on by default.
import { check, docker, finish, signIn, sleep, stateOf, waitFor } from '../lib.mjs'

const [host, exit, wan] = process.argv.slice(2)
const routed = '1.1.1.1'
const direct = '1.1.1.2'
const tunneled = '1.1.1.3'
const names = ['host', 'exit'].flatMap((source) => ['routed', 'direct', 'tunneled']
  .flatMap((target) => [`${source} ICMP ${target}`, `${source} TCP ${target}`]))

const addressOf = (container) => docker('inspect', '-f', '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}', container).trim()
const inHost = (...command) => docker('exec', host, ...command)

// NOTE: The chain STAND of the wan container counts packets in the order of check.sh.
function counters() {
  const lines = docker('exec', wan, 'iptables', '-nvx', '-L', 'STAND').split('\n').slice(2).filter(Boolean)
  return Object.fromEntries(lines.map((line, index) => [names[index], Number(line.trim().split(/\s+/)[0])]))
}

async function measure(...command) {
  const before = counters()
  const output = inHost(...command)
  await sleep(500)
  const after = counters()
  const sent = Object.fromEntries(names.map((name) => [name, after[name] - before[name]])
    .filter(([, count]) => count !== 0))
  return { output, sent }
}

const connect = (target) => measure('bash', '-c', `timeout 5 bash -c 'exec 3<>/dev/tcp/${target}/80' && echo connected`)
const ping = (target) => measure('ping', '-c', '3', '-W', '2', target)
const pingChain = () => inHost('iptables', '-S', 'PROXIMITY-PING')
const answered = (output) => /\b3 received/.test(output)
const silent = (output) => /\b0 received/.test(output)
const summary = (output) => output.split('\n').filter((line) => /transmitted|rtt|filtered|denied|unreachable/i.test(line)).slice(-2).join(' | ')

function report(label, result) {
  console.log(`${label}: ${JSON.stringify(result.sent)} ${summary(result.output)}`)
  return result
}

async function settle(client, change, test) {
  const current = await client.call('GET', '/api/settings')
  await client.call('PUT', '/api/settings', { ...current, ...change })
  return Boolean(await waitFor(test, 30))
}

async function run() {
  const client = await signIn(host)
  await settle(client, { capture: true, probe_target: `${routed}:80` }, () => true)
  const far = await signIn(exit)
  await far.call('POST', '/api/inbounds', { id: 'Exit', type: 'socks5', enabled: true, autostart: true,
    statistics: true, port: 7800, outbound: '@host', anonymous: true, users: [], socks5: { listen: '0.0.0.0', udp: true } })
  await far.call('POST', '/api/inbounds', { id: 'VPN', type: 'openvpn', enabled: true, autostart: true,
    statistics: true, port: 7801, outbound: '@host', anonymous: false,
    users: [{ enabled: true, name: 'alice', password: 'wonderland' }],
    openvpn: { server: addressOf(exit), protocol: 'udp', otp: false } })
  check('exit inbounds start', Boolean(await waitFor(async () => (await stateOf(far, 'inbounds', 'Exit')).state === 'up' &&
    (await stateOf(far, 'inbounds', 'VPN')).state === 'up', 90)))
  const config = (await far.call('GET', '/api/inbounds/VPN/config?user=alice')).config
  await client.call('POST', '/api/outbounds', { id: 'Exit', type: 'socks5', enabled: true, autostart: true,
    statistics: true, socks5: { server: addressOf(exit), port: 7800, udp: true } })
  await client.call('POST', '/api/outbounds', { id: 'Tunnel', type: 'openvpn', enabled: true, autostart: true,
    attempts: 3, reconnect_timeout: 3, statistics: true,
    openvpn: { config, username: 'alice', password: 'wonderland', use_dns: false, use_routes: false },
    monitoring: { enabled: false, interval: 30, timeout: 5, retries: 3, target: `${routed}:80` } })
  await client.call('POST', '/api/routes', { directive: 'IP-CIDR', target: 'Exit', enabled: true, params: [`${routed}/32`] })
  await client.call('POST', '/api/routes', { directive: 'IP-CIDR', target: 'Tunnel', enabled: true, params: [`${tunneled}/32`] })
  check('capture rules are installed', Boolean(await waitFor(() => inHost('ip', 'rule').includes('5170'), 60)))
  check('tunnel outbound connects', Boolean(await waitFor(async () => (await stateOf(client, 'outbounds', 'Tunnel')).state === 'up', 90)))
  check('ping filter is installed', Boolean(await waitFor(() => pingChain().includes(`-d ${routed}/32 -j DROP`), 30)))
  check('ping of the tunnel route is routed into the tunnel', Boolean(await waitFor(() => /5165:\s+from all to 1\.1\.1\.3 ipproto icmp lookup 51\d{3}/.test(inHost('ip', 'rule')), 30)))

  const tcpRouted = report(`TCP  ${routed}`, await connect(routed))
  check('TCP to the SOCKS5 route leaves through the exit', tcpRouted.sent['exit TCP routed'] > 0 && !tcpRouted.sent['host TCP routed'])
  const tcpDirect = report(`TCP  ${direct}`, await connect(direct))
  check('TCP to an address without a route leaves directly', tcpDirect.sent['host TCP direct'] > 0)
  const tcpTunnel = report(`TCP  ${tunneled}`, await connect(tunneled))
  check('TCP to the tunnel route leaves through the tunnel', tcpTunnel.sent['exit TCP tunneled'] > 0 && !tcpTunnel.sent['host TCP tunneled'])

  const pingRouted = report(`ICMP ${routed}`, await ping(routed))
  check('ping of the SOCKS5 route does not leave past the route', !pingRouted.sent['host ICMP routed'])
  check('ping of the SOCKS5 route gets no false answer', silent(pingRouted.output))
  const pingDirect = report(`ICMP ${direct}`, await ping(direct))
  check('ping of an address without a route leaves directly and gets real answers',
    pingDirect.sent['host ICMP direct'] === 3 && answered(pingDirect.output))
  const pingTunnel = report(`ICMP ${tunneled}`, await ping(tunneled))
  check('ping of the tunnel route goes through the tunnel and gets real answers',
    pingTunnel.sent['exit ICMP tunneled'] === 3 && !pingTunnel.sent['host ICMP tunneled'] && answered(pingTunnel.output))

  check('ping filter follows the default target (SOCKS5)', await settle(client, { default_target: 'Exit' },
    () => pingChain().trim().endsWith('-A PROXIMITY-PING -j DROP')))
  const pingDefault = report(`ICMP ${direct} (default target Exit)`, await ping(direct))
  check('ping of an address routed by the default target does not leave directly', !pingDefault.sent['host ICMP direct'])

  check('ping follows the default target (tunnel)', await settle(client, { default_target: 'Tunnel' },
    () => /5166:\s+from all ipproto icmp lookup 51\d{3}/.test(inHost('ip', 'rule'))))
  const pingDefaultTunnel = report(`ICMP ${direct} (default target Tunnel)`, await ping(direct))
  check('ping of an address routed by the default tunnel goes through the tunnel',
    pingDefaultTunnel.sent['exit ICMP direct'] === 3 && !pingDefaultTunnel.sent['host ICMP direct'] && answered(pingDefaultTunnel.output))
  const lan = inHost('ping', '-c', '3', '-W', '2', addressOf(exit))
  check('ping of a LAN address stays on the LAN with the default tunnel', answered(lan) &&
    inHost('ip', 'route', 'get', addressOf(exit), 'ipproto', 'icmp').includes('dev eth0'))

  check('ping filter is removed when the setting is off', await settle(client, { default_target: 'DIRECT', ping_filter: false },
    () => !inHost('iptables', '-S', 'OUTPUT').includes('PROXIMITY-PING') && !/516[56]:/.test(inHost('ip', 'rule'))))
  const pingOff = report(`ICMP ${routed} (setting off)`, await ping(routed))
  check('with the setting off ping of the SOCKS5 route leaves directly', pingOff.sent['host ICMP routed'] === 3)
}

run().then(finish).catch((error) => {
  console.error(`FAIL  ${error.message}`)
  process.exit(1)
})
