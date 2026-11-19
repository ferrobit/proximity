// Checks of the stand "tools": the address check and the ping of the page Tools.
// NOTE: check.sh copies this file next to the tools of the screenshots and runs it there.
import path from 'node:path'
import { pathToFileURL } from 'node:url'
import puppeteer from 'puppeteer-core'
import { findBrowser } from './browser.mjs'

const { account, check, docker, finish, signIn, stateOf, waitFor } = await import(pathToFileURL(process.env.STAND_LIB).href)
const [container, exitContainer, exitAddress, webAddress] = process.argv.slice(2)
const address = () => `http://${docker('port', container, '8899/tcp').split('\n')[0].trim()}`
const pictures = process.env.STAND_PICTURES

async function lookup(client, body) {
  return client.call('POST', '/api/tools/lookup', body)
}

function outbound(result, id) {
  return result.outbounds.find((item) => item.id === id) || { resolvers: [], connect: { error: 'missing' }, replies: [] }
}

async function prepare() {
  const exit = await signIn(exitContainer)
  await exit.call('POST', '/api/inbounds', { type: 'socks5', enabled: true, autostart: true, anonymous: true, users: [] })
  const client = await signIn(container)
  await client.call('POST', '/api/outbounds', { type: 'host', enabled: true, autostart: true })
  await client.call('POST', '/api/outbounds', { type: 'socks5', enabled: true, autostart: true,
    socks5: { server: exitAddress, port: 7800 }, dns: { servers: ['tcp://127.0.0.11'], exit: '' } })
  await client.call('POST', '/api/routes', { id: 'Web', directive: 'DOMAIN-SUFFIX', target: 'SOCKS5 1', enabled: true, params: ['tools.test'] })
  await client.call('POST', '/api/users', { name: 'viewer', password: 'Viewer-pass-1', confirm: 'Viewer-pass-1', role: 'user' })
  check('outbounds are up', Boolean(await waitFor(async () => (await stateOf(client, 'outbounds', 'HOST 1')).state === 'up' &&
    (await stateOf(client, 'outbounds', 'SOCKS5 1')).state === 'up', 60)))
  check('route is applied', Boolean(await waitFor(async () => (await client.call('GET', '/api/router/config')).config.includes('tools.test'), 20)))
  return client
}

async function checkApi(client) {
  const found = await lookup(client, { host: 'web.tools.test', port: 8080 })
  check('route of the name', found.route.name === 'Web' && found.route.rule === 'DomainSuffix(tools.test)', JSON.stringify(found.route))
  check('chain goes through SOCKS5 1', found.route.chain.join() === 'SOCKS5 1' && !found.route.error && found.route.latency > 0 &&
    found.route.proxied)
  check('router answers the address', found.addresses.includes(webAddress), found.addresses.join())
  check('every outbound is listed', found.outbounds.map((item) => item.id).join() === 'DIRECT,HOST 1,SOCKS5 1')
  check('every outbound connects', found.outbounds.every((item) => !item.connect.error && item.connect.latency > 0),
    JSON.stringify(found.outbounds.map((item) => item.connect)))
  const socks = outbound(found, 'SOCKS5 1').resolvers[0] || {}
  check('DNS of SOCKS5 1 goes through it', socks.server === 'tcp://127.0.0.11#SOCKS5 1' && socks.addresses?.includes(webAddress),
    JSON.stringify(socks))
  check('DIRECT asks the system resolver', outbound(found, 'DIRECT').resolvers[0]?.server === 'system')

  const closed = await lookup(client, { host: 'web.tools.test:8081' })
  check('closed port is reported', closed.port === 8081 && /closed by the far side/.test(closed.route.error) &&
    closed.route.name === 'Web',
    JSON.stringify(closed.route))
  check('closed port fails on every outbound', closed.outbounds.every((item) => item.connect.error))
  const unknown = await lookup(client, { host: 'nowhere.invalid' })
  check('unknown name: no answer, default route', unknown.problem !== '' && unknown.route.name === 'Everything else', JSON.stringify(unknown.route))
  const url = await lookup(client, { host: 'http://web.tools.test:8080/index.html' })
  check('URL gives host and port', url.host === 'web.tools.test' && url.port === 8080 && !url.route.error)
  const literal = await lookup(client, { host: webAddress, port: 8080 })
  check('address: no DNS, default route', literal.outbounds.every((item) => item.resolvers.length === 0) &&
    literal.route.name === 'Everything else')

  const pinged = await client.call('POST', '/api/tools/ping', { host: 'web.tools.test', count: 3, port: 8080 })
  check('ping resolves the name', pinged.address === webAddress)
  check('ICMP through DIRECT and HOST 1', ['DIRECT', 'HOST 1'].every((id) => outbound(pinged, id).method === 'icmp' &&
    outbound(pinged, id).replies.length === 3), JSON.stringify(pinged.outbounds.map((item) => [item.id, item.replies.length, item.error])))
  check('TCP through SOCKS5 1', outbound(pinged, 'SOCKS5 1').method === 'tcp' && outbound(pinged, 'SOCKS5 1').replies.length === 3)
  const absent = await client.call('POST', '/api/tools/ping', { host: '10.213.78.250', count: 2, port: 8080 })
  check('absent address: no ICMP replies', absent.outbounds.filter((item) => item.method === 'icmp')
    .every((item) => item.replies.length === 0 && /100% packet loss/.test(item.error)),
    JSON.stringify(absent.outbounds.map((item) => item.error)))
  const viewer = await signIn(container, { username: 'viewer', password: 'Viewer-pass-1' }).catch(() => null)
  const denied = viewer ? await viewer.call('POST', '/api/tools/lookup', { host: 'web.tools.test' }).then(() => false, () => true) : false
  check('a viewer cannot run the tools', denied)
}

async function checkPage() {
  const browser = await puppeteer.launch({ executablePath: findBrowser(), headless: true,
    args: ['--no-sandbox', '--no-proxy-server'] })
  try {
    const page = await browser.newPage()
    await page.setViewport({ width: 1440, height: 900, deviceScaleFactor: 2 })
    await page.goto(address(), { waitUntil: 'networkidle0' })
    await page.evaluate(async (credentials) => {
      await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'stand' },
        body: JSON.stringify(credentials) })
    }, account)
    await page.goto(`${address()}/#/tools`, { waitUntil: 'networkidle0' })
    await page.reload({ waitUntil: 'networkidle0' })
    const menu = await page.evaluate(() => [...document.querySelectorAll('.ant-menu-item')].map((item) => item.innerText.trim()))
    check('Tools comes before Settings', menu.indexOf('Tools') >= 0 && menu.indexOf('Tools') === menu.indexOf('Settings') - 1, menu.join())
    await page.type('.lookup-card .host-input input', 'web.tools.test:8080')
    await page.click('.lookup-card button[type=submit]')
    const trail = await waitFor(() => page.evaluate(() => document.querySelector('.route-path')?.innerText || ''), 20)
    check('route path is drawn', /Web/.test(trail || '') && /SOCKS5 1/.test(trail || '') && /accepted by the proxy in|connected in/.test(trail || ''),
      (trail || '').replace(/\s+/g, ' '))
    await page.type('.ping-card .host-input input', 'web.tools.test')
    await page.click('.ping-card .ant-input-number input')
    await page.keyboard.down('Control')
    await page.keyboard.press('KeyA')
    await page.keyboard.up('Control')
    await page.keyboard.press('Backspace')
    await page.type('.ping-card .ant-input-number input', '8080')
    await page.click('.ping-card button[type=submit]')
    const nodes = await waitFor(() => page.evaluate(() => document.querySelectorAll('.ping-map g.node').length), 30)
    check('ping map is drawn', nodes === 5, String(nodes))
    const labels = await page.evaluate(() => [...document.querySelectorAll('.ping-map .edge-label')].map((item) => item.textContent))
    check('every way answers on the page', labels.length === 3 && labels.every((text) => / · 0% loss$/.test(text)), labels.join(' | '))
    await page.evaluate(() => document.activeElement?.blur())
    if (pictures) {
      const lookupCard = await page.$('.lookup-card')
      await lookupCard.screenshot({ path: path.join(pictures, 'tools-lookup.png') })
      const pingCard = await page.$('.ping-card')
      await pingCard.screenshot({ path: path.join(pictures, 'tools-ping.png') })
    }
  } finally {
    await browser.close()
  }
}

try {
  const client = await prepare()
  await checkApi(client)
  await checkPage()
} catch (error) {
  check('stand run', false, error.stack)
}
finish()
