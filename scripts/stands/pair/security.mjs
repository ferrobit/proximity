// Checks of the OpenVPN inbound security on the stand "pair": single use of one-time codes,
// rejection of an old client certificate, ping of clients past the routes.
import { check, docker, finish, logOf, signIn, sleep, stateOf, waitFor } from '../lib.mjs'

const server = 'proximity-stand-b'
const client = 'proximity-stand-a'
const step = 30

function makeInbound(material) {
  return {
    id: 'VPN', type: 'openvpn', enabled: true, autostart: true, statistics: true, port: 7800,
    outbound: '@router', anonymous: false,
    users: [{ enabled: true, name: 'alice', password: 'wonderland' },
      { enabled: true, name: 'bob', password: 'builder-1' }],
    openvpn: {
      server, protocol: 'udp', network: '10.66.0.0/24', otp: true, otp_key: material.otp_key,
      ca_certificate: material.ca_certificate, ca_key: material.ca_key,
      certificate: material.certificate, key: material.key,
      client_certificate: material.client_certificate, client_key: material.client_key,
      tls_crypt: material.tls_crypt
    }
  }
}

function makeOutbound(id, name, password, key, config) {
  return {
    id, type: 'openvpn', enabled: true, autostart: true, attempts: 3, reconnect_timeout: 3,
    statistics: true, openvpn: { config, username: name, password, otp_key: key, otp_mode: 'challenge', use_dns: false },
    monitoring: { enabled: false, interval: 30, timeout: 5, retries: 3, target: '1.1.1.1:443' }
  }
}

async function exportConfig(far, name) {
  return (await far.call('GET', `/api/inbounds/VPN/config?user=${name}`)).config
}

async function run() {
  const far = await signIn(server)
  const near = await signIn(client)
  const material = await far.call('POST', '/api/inbounds/generate', { kind: 'all' })
  await far.call('POST', '/api/inbounds', makeInbound(material))
  check('server starts', await waitFor(async () => (await stateOf(far, 'inbounds', 'VPN')).state === 'up', 60))
  const serverText = docker('exec', server, 'sh', '-c', 'cat /run/proximity/inbound-1/server.conf')
  check('server accepts only the current client certificate', /^peer-fingerprint ([0-9A-F]{2}:){31}[0-9A-F]{2}$/m.test(serverText))

  // One-time codes: the checks start at the beginning of a code period.
  await sleep((step - Math.floor(Date.now() / 1000) % step + 1) * 1000)
  const config = await exportConfig(far, 'alice')
  await near.call('POST', '/api/outbounds', makeOutbound('Alice', 'alice', 'wonderland', material.otp_key, config))
  check('first use of a code is accepted', await waitFor(async () => (await stateOf(near, 'outbounds', 'Alice')).state === 'up', 20))
  await near.retry('POST', '/api/outbounds/Alice/restart')
  await sleep(1500)
  check('same user from the same address may reconnect with the same code',
    await waitFor(async () => (await stateOf(near, 'outbounds', 'Alice')).state === 'up', 15))
  await near.call('POST', '/api/outbounds', makeOutbound('Bob', 'bob', 'builder-1', material.otp_key, config))
  const refused = await waitFor(async () => (await logOf(far, 'inbounds', 'VPN')).includes("one-time code of user 'bob' is already used"), 20)
  check('another user is refused with the used code', Boolean(refused))
  const waiting = await waitFor(async () => /waiting \d+s for a new one-time code/.test(await logOf(near, 'outbounds', 'Bob')), 20)
  check('refused client waits for a new code', Boolean(waiting))
  check('refused client connects with the next code', await waitFor(async () => (await stateOf(near, 'outbounds', 'Bob')).state === 'up', 60))
  check('first user was not refused', !(await logOf(far, 'inbounds', 'VPN')).includes("one-time code of user 'alice' is already used"))

  // Ping of clients.
  const device = (await stateOf(near, 'outbounds', 'Alice')).device
  const gateway = docker('network', 'inspect', '-f', '{{(index .IPAM.Config 0).Gateway}}', 'proximity-stand').trim()
  const inside = docker('exec', client, 'ping', '-c', '2', '-W', '2', '-I', device, '10.66.0.1')
  check('ping of the server tunnel address works', inside.includes(' 0% packet loss'), device)
  const outside = docker('exec', client, 'ping', '-c', '2', '-W', '2', '-I', device, gateway)
  check('ping of another host through the server is rejected', /filtered|Prohibited/i.test(outside) && outside.includes('100% packet loss'),
    outside.split('\n').find((line) => line.startsWith('From')) || outside.split('\n')[1])
  const rules = docker('exec', server, 'iptables', '-nvL', 'FORWARD')
  const rule = rules.split('\n').find((line) => line.includes('icmp-admin-prohibited')) || ''
  check('reject rule stands first and has counted the pings', rules.split('\n')[2] === rule && Number(rule.trim().split(/\s+/)[0]) >= 2, rule.trim())
  const inbound = (await far.call('GET', '/api/inbounds')).inbounds.find((item) => item.id === 'VPN')
  await far.call('PUT', '/api/inbounds/VPN', { ...inbound, outbound: '@host' })
  await sleep(2000)
  check('rule is removed for an inbound that goes straight to the host', !docker('exec', server, 'iptables', '-nvL', 'FORWARD').includes('icmp-admin-prohibited'))
  await far.call('PUT', '/api/inbounds/VPN', { ...inbound, outbound: '@router' })
  await sleep(2000)
  check('rule is back for an inbound that goes to the router', docker('exec', server, 'iptables', '-nvL', 'FORWARD').includes('icmp-admin-prohibited'))

  // Certificate: a regenerated client certificate replaces the old one.
  const renewed = await far.call('POST', '/api/inbounds/generate',
    { kind: 'client', ca_certificate: material.ca_certificate, ca_key: material.ca_key })
  const current = (await far.call('GET', '/api/inbounds')).inbounds.find((item) => item.id === 'VPN')
  await far.call('PUT', '/api/inbounds/VPN', { ...current,
    openvpn: { ...current.openvpn, client_certificate: renewed.client_certificate, client_key: renewed.client_key } })
  await waitFor(async () => (await stateOf(far, 'inbounds', 'VPN')).state === 'up', 60)
  await near.retry('POST', '/api/outbounds/Alice/restart')
  await sleep(3000)
  const marker = 'client certificate is rejected: it is not the current client certificate'
  const rejected = await waitFor(async () => (await logOf(far, 'inbounds', 'VPN')).includes(marker), 90)
  check('server log tells about the rejected certificate', Boolean(rejected))
  const old = await stateOf(near, 'outbounds', 'Alice')
  check('client with the old certificate does not connect', old.state !== 'up', `${old.state}: ${old.message}`)
  const outbound = (await near.call('GET', '/api/outbounds')).outbounds.find((item) => item.id === 'Alice')
  await near.call('PUT', '/api/outbounds/Alice', { ...outbound, openvpn: { ...outbound.openvpn, config: await exportConfig(far, 'alice') } })
  check('client with the new certificate connects', await waitFor(async () => (await stateOf(near, 'outbounds', 'Alice')).state === 'up', 90))
  finish()
}

run().catch((error) => {
  console.error(`FAIL  ${error.message}`)
  process.exit(1)
})
