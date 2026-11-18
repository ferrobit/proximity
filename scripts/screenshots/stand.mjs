import { readFile } from 'node:fs/promises'
import http from 'node:http'
import net from 'node:net'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const folder = path.dirname(fileURLToPath(import.meta.url))
const startTimeout = 90
const linkTimeout = 120
const targetPort = 8899

export const account = {
  username: process.env.SCREENSHOT_USER || 'admin',
  password: process.env.SCREENSHOT_PASSWORD || ''
}

export function sleep(time) {
  return new Promise((resolve) => setTimeout(resolve, time))
}

export async function readSeed() {
  return JSON.parse(await readFile(path.join(folder, 'seed.json'), 'utf8'))
}

export class Client {
  constructor(base) {
    this.base = base
    this.cookie = ''
  }

  async call(method, target, body) {
    const response = await fetch(this.base + target, {
      method,
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'screenshots', Cookie: this.cookie },
      body: body === undefined ? undefined : JSON.stringify(body)
    })
    const text = await response.text()
    const cookies = response.headers.getSetCookie()
    if (cookies.length !== 0) this.cookie = cookies.map((item) => item.split(';')[0]).join('; ')
    if (response.status !== 200) {
      throw new Error(`${method} ${this.base}${target} failed (${response.status}): ${text.trim()}`)
    }
    return text ? JSON.parse(text) : null
  }

  // NOTE: A new outbound or inbound is known to the control loop a moment after the answer.
  async retry(method, target, body) {
    for (let attempt = 0; ; attempt++) {
      try {
        return await this.call(method, target, body)
      } catch (error) {
        if (attempt === 20) throw error
        await sleep(500)
      }
    }
  }

  async waitForServer() {
    for (let attempt = 0; attempt < startTimeout; attempt++) {
      try {
        if ((await fetch(`${this.base}/api/session`)).ok) return
      } catch {
        // NOTE: The server is not listening yet.
      }
      await sleep(1000)
    }
    throw new Error(`Proximity at ${this.base} did not answer in ${startTimeout} seconds`)
  }

  async addMissing(section, items, list) {
    const present = (list || (await this.call('GET', `/api/${section}`))[section]).map((item) => item.id)
    for (const item of items) {
      if (!present.includes(item.id)) await this.call('POST', `/api/${section}`, item)
    }
  }
}

// NOTE: The far side is a second Proximity: its inbounds are the SOCKS5 proxy and the
// OpenVPN server of the main instance, its proxy port is the HTTP proxy, its web UI is the
// "site" that the generated traffic downloads.
export async function seedPeer(peer, seed) {
  await peer.call('POST', '/api/login', account)
  await peer.addMissing('inbounds', seed.peer.inbounds)
  const tunnel = seed.peer.inbounds.find((item) => item.type === 'openvpn')
  const user = tunnel.users[0].name
  const target = `/api/inbounds/${encodeURIComponent(tunnel.id)}/config?user=${encodeURIComponent(user)}`
  return (await peer.call('GET', target)).config
}

export async function seedMain(main, seed, tunnelConfig) {
  if ((await main.call('GET', '/api/session')).setup) {
    await main.call('POST', '/api/setup', { ...account, confirm: account.password })
  }
  await main.call('POST', '/api/login', account)
  const outbounds = seed.main.outbounds.map((item) => item.type !== 'openvpn' ? item
    : { ...item, openvpn: { ...item.openvpn, config: tunnelConfig } })
  const present = (await main.call('GET', '/api/outbounds')).outbounds.map((item) => item.id)
  await main.addMissing('outbounds', outbounds)
  for (const item of outbounds) {
    // NOTE: An outbound with manual start is shown as stopped, as after a restart.
    if (!item.autostart && !present.includes(item.id)) {
      await main.retry('POST', `/api/outbounds/${encodeURIComponent(item.id)}/stop`)
    }
  }
  await main.addMissing('fallbacks', seed.main.fallbacks, await main.call('GET', '/api/fallbacks'))
  await main.addMissing('routes', seed.main.routes)
  await main.call('PUT', '/api/routes/default', { target: seed.main.default_target })
  await main.addMissing('inbounds', seed.main.inbounds)
  const users = (await main.call('GET', '/api/users')).map((item) => item.name)
  for (const item of seed.main.users) {
    if (!users.includes(item.name)) await main.call('POST', '/api/users', { ...item, confirm: item.password })
  }
}

export async function waitForLinks(main, seed) {
  const wanted = seed.main.outbounds.filter((item) => item.enabled && item.autostart).map((item) => item.id)
  let states = {}
  for (let attempt = 0; attempt < linkTimeout; attempt++) {
    const outbounds = (await main.call('GET', '/api/outbounds')).outbounds
    states = Object.fromEntries(outbounds.map((item) => [item.id, item.status.state]))
    if (wanted.every((id) => states[id] === 'up')) return
    await sleep(1000)
  }
  throw new Error(`outbounds are not up in ${linkTimeout} seconds: ${JSON.stringify(states)}`)
}

function requestThroughProxy(proxy, host, target) {
  return new Promise((resolve) => {
    const [address, port] = proxy.split(':')
    const request = http.get({
      host: address, port: Number(port), path: `http://${host}${target}`, headers: { Host: host }, agent: false
    }, (response) => {
      response.resume()
      response.on('end', resolve)
    })
    request.setTimeout(20000, () => request.destroy())
    request.on('error', resolve)
    request.on('close', resolve)
  })
}

function holdThroughProxy(proxy, host, time) {
  return new Promise((resolve) => {
    const [address, port] = proxy.split(':')
    const socket = net.connect(Number(port), address, () => {
      socket.write(`CONNECT ${host} HTTP/1.1\r\nHost: ${host}\r\n\r\n`)
    })
    socket.on('data', () => {})
    socket.on('error', () => {})
    socket.on('close', resolve)
    setTimeout(() => socket.destroy(), time)
  })
}

function requestThroughSocks(server, user, host, port, target) {
  return new Promise((resolve) => {
    const [address, serverPort] = server.split(':')
    const socket = net.connect(Number(serverPort), address)
    let step = 0
    socket.on('connect', () => socket.write(Buffer.from([5, 1, 2])))
    socket.on('data', () => {
      step++
      if (step === 1) {
        const name = Buffer.from(user.name)
        const password = Buffer.from(user.password)
        socket.write(Buffer.concat([Buffer.from([1, name.length]), name,
          Buffer.from([password.length]), password]))
      } else if (step === 2) {
        const domain = Buffer.from(host)
        socket.write(Buffer.concat([Buffer.from([5, 1, 0, 3, domain.length]), domain,
          Buffer.from([port >> 8, port & 255])]))
      } else if (step === 3) {
        socket.write(`GET ${target} HTTP/1.1\r\nHost: ${host}:${port}\r\nConnection: close\r\n\r\n`)
      }
    })
    socket.setTimeout(20000, () => socket.destroy())
    socket.on('error', () => {})
    socket.on('close', resolve)
  })
}

function between(low, high) {
  return low + Math.random() * (high - low)
}

async function repeat(state, low, high, action) {
  await sleep(between(0, high) * 1000)
  while (!state.stopped) {
    await action()
    await sleep(between(low, high) * 1000)
  }
}

// NOTE: The traffic goes through the proxy port and the SOCKS5 inbound of the main instance
// to names that the routes send to different outbounds; all names lead to the far side.
export async function startTraffic(peer, seed, proxy, socks) {
  const page = await (await fetch(`${peer.base}/`)).text()
  const large = `/${page.match(/assets\/[^"]+\.js/)[0]}`
  const small = `/${page.match(/assets\/[^"]+\.css/)[0]}`
  const user = seed.main.inbounds.find((item) => item.type === 'socks5').users[0]
  const state = { stopped: false, started: Date.now() }
  const site = (name) => `${name}:${targetPort}`
  for (const flow of seed.traffic.flows) {
    repeat(state, flow.pause[0], flow.pause[1], () => flow.socks
      ? requestThroughSocks(socks, user, flow.host, targetPort, flow.large ? large : small)
      : requestThroughProxy(proxy, site(flow.host), flow.large ? large : small))
  }
  for (const host of seed.traffic.held) {
    repeat(state, 1, 6, () => holdThroughProxy(proxy, site(host), between(4, 25) * 1000))
  }
  for (const target of seed.traffic.unreachable) requestThroughProxy(proxy, target, '/')
  return state
}
