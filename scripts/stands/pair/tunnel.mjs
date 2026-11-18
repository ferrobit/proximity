// Checks of an OpenVPN link on the stand "pair": defaults of a new inbound, the route that
// follows the routes of the VPN connection, a disabled user, the saved one-time code after a
// restart of the inbound, the instance mark that makes an open page reload itself.
import { check, docker, finish, logOf, signIn, sleep, stateOf, waitFor } from '../lib.mjs'

const server = 'proximity-stand-b'
const client = 'proximity-stand-a'
const header = 'x-proximity-instance'
const pushed = ['10.20.0.0/16', '10.30.0.0/16']

function makeInbound() {
  return {
    id: 'VPN', type: 'openvpn', enabled: true, autostart: true, statistics: true, port: 7800,
    outbound: '@router', anonymous: false,
    users: [{ enabled: true, name: 'alice', password: 'wonderland' },
      { enabled: true, name: 'bob', password: 'builder-1' }],
    openvpn: { server, protocol: 'udp', otp: true, routes: pushed }
  }
}

function makeOutbound(key, config) {
  return {
    id: 'Alice', type: 'openvpn', enabled: true, autostart: true, attempts: 3, reconnect_timeout: 3,
    statistics: true, openvpn: { config, username: 'alice', password: 'wonderland', otp_key: key,
      otp_mode: 'challenge', use_dns: false, use_routes: true },
    monitoring: { enabled: false, interval: 30, timeout: 5, retries: 3, target: '1.1.1.1:443' }
  }
}

async function instanceOf(container) {
  const address = docker('port', container, '8899/tcp').split('\n')[0].trim()
  return (await fetch(`http://${address}/api/session`)).headers.get(header) || ''
}

async function routesOf(near) {
  return (await near.call('GET', '/api/routes')).routes
}

async function providedOf(near) {
  const routes = await routesOf(near)
  const index = routes.findIndex((route) => route.provided === 'Alice')
  return index < 0 ? null : { ...routes[index], index }
}

const usedCodes = (name) => docker('exec', server, 'sh', '-c', `cat /run/proximity/inbound-1/${name} 2>/dev/null || echo none`).trim()
const up = (side, section, id, timeout) => waitFor(async () => (await stateOf(side, section, id)).state === 'up', timeout)

async function run() {
  const far = await signIn(server)
  let near = await signIn(client)

  // Defaults of a new inbound.
  const created = await far.call('POST', '/api/inbounds', makeInbound())
  const network = created.openvpn.network
  const second = Number(network.split('.')[1])
  check('default network of an inbound depends on the instance', /^10\.\d+\.1\.0\/24$/.test(network) && second >= 64 && second < 192, network)
  check('default tunnel MTU is 1420', created.openvpn.mtu === 1420, String(created.openvpn.mtu))
  check('server starts', await up(far, 'inbounds', 'VPN', 60))
  const text = docker('exec', server, 'sh', '-c', 'cat /run/proximity/inbound-1/server.conf')
  check('server has a lasting key of tokens and checks the user of a token', /^auth-gen-token 0 external-auth$/m.test(text) &&
    /^auth-gen-token-secret \S+token\.key$/m.test(text) && usedCodes('token.key').includes('OpenVPN auth-token server key'))

  // The route that follows the routes of the VPN connection.
  await near.call('POST', '/api/routes', { directive: 'DOMAIN', target: 'DIRECT', enabled: true, params: ['example.test'] })
  const material = (await far.call('GET', '/api/inbounds')).inbounds.find((item) => item.id === 'VPN').openvpn
  const config = (await far.call('GET', '/api/inbounds/VPN/config?user=alice')).config
  check('exported client configuration uses MTU 1420', /^tun-mtu 1420$/m.test(config))
  await near.call('POST', '/api/outbounds', makeOutbound(material.otp_key, config))
  check('client connects', await up(near, 'outbounds', 'Alice', 60))
  const route = await waitFor(async () => {
    const found = await providedOf(near)
    return found && found.enabled ? found : null
  }, 20)
  check('route with the pushed networks appears first and is on', Boolean(route) && route.index === 0 &&
    pushed.every((item) => route.params.includes(item)), route ? route.params.join(' ') : 'no route')
  let refused = ''
  try {
    await near.call('PUT', `/api/routes/${encodeURIComponent(route.key)}`, { ...route, enabled: false })
  } catch (error) {
    refused = error.message
  }
  check('route cannot be switched by hand', refused.includes('provided route follows its VPN connection'))
  await near.call('POST', `/api/routes/${encodeURIComponent(route.key)}/move`, { delta: 1 })
  check('route can be moved', (await providedOf(near)).index === 1)
  await near.call('POST', '/api/outbounds/Alice/stop')
  const stopped = await waitFor(async () => {
    const found = await providedOf(near)
    return found && !found.enabled ? found : null
  }, 20)
  check('route is off while the connection is down, keeps its place and networks', Boolean(stopped) && stopped.index === 1 &&
    pushed.every((item) => stopped.params.includes(item)))

  // The instance mark and the saved position after a restart of the container.
  const before = await instanceOf(client)
  check('every answer carries the same instance mark', before !== '' && before === await instanceOf(client), before)
  docker('restart', client)
  near = await signIn(client)
  const after = await instanceOf(client)
  check('instance mark changes after a restart', after !== '' && after !== before, `${before} -> ${after}`)
  const kept = await providedOf(near)
  check('route keeps its place after a restart, still off', Boolean(kept) && kept.index === 1 && !kept.enabled)
  await near.retry('POST', '/api/outbounds/Alice/start')
  check('client connects when it is started again', await up(near, 'outbounds', 'Alice', 90))
  check('route is on again', Boolean(await waitFor(async () => (await providedOf(near)).enabled, 20)))

  // A restart of the inbound: the client goes on with its token, no new one-time code.
  await sleep(3000)
  check('one-time code of the client is written down as used', usedCodes('codes.json').includes('alice'))
  await far.call('POST', '/api/inbounds/VPN/restart')
  await sleep(5000)
  check('server is up after its restart', await up(far, 'inbounds', 'VPN', 60))
  const back = await waitFor(async () => (await stateOf(far, 'inbounds', 'VPN')).clients?.length === 1 &&
    (await stateOf(near, 'outbounds', 'Alice')).state === 'up', 90)
  check('client is back after the restart of the server', Boolean(back))
  // NOTE: The list of used codes is empty after a restart: a login with a code would fill it.
  const codes = usedCodes('codes.json')
  check('client came back with its token, without a new one-time code', codes === 'none' &&
    !(await logOf(far, 'inbounds', 'VPN')).includes('authentication failed'), codes)

  // A disabled user is told to stop at once.
  const inbound = (await far.call('GET', '/api/inbounds')).inbounds.find((item) => item.id === 'VPN')
  const started = Date.now()
  await far.call('PUT', '/api/inbounds/VPN', { ...inbound, users: [{ ...inbound.users[0], enabled: false }, inbound.users[1]] })
  const gone = await waitFor(async () => (await stateOf(near, 'outbounds', 'Alice')).state !== 'up', 30)
  const time = Math.round((Date.now() - started) / 1000)
  check('client of a disabled user notices it within 10 seconds', Boolean(gone) && time <= 10, `${time} s`)
  check('server told the client to halt', (await logOf(far, 'inbounds', 'VPN')).includes('client-kill command succeeded'))
  const final = await waitFor(async () => {
    const found = await providedOf(near)
    return found && !found.enabled
  }, 30)
  check('route is off after the user was disabled', Boolean(final))

  // Without "Receive routes" there is no such route.
  const outbound = (await near.call('GET', '/api/outbounds')).outbounds.find((item) => item.id === 'Alice')
  await near.call('PUT', '/api/outbounds/Alice', { ...outbound, openvpn: { ...outbound.openvpn, use_routes: false } })
  check('route is removed when the routes are not received', Boolean(await waitFor(async () => (await providedOf(near)) === null, 20)))
  check('other routes stay', (await routesOf(near)).length === 1)
  finish()
}

run().catch((error) => {
  console.error(`FAIL  ${error.message}`)
  process.exit(1)
})
