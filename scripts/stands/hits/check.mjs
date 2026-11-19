// Checks of the stand "hits": route hit counters read from mihomo, their history and the chart.
// NOTE: check.sh copies this file next to the tools of the screenshots and runs it there.
import http from 'node:http'
import { pathToFileURL } from 'node:url'
import puppeteer from 'puppeteer-core'
import { findBrowser } from './browser.mjs'

const { check, docker, finish, signIn, sleep, waitFor } = await import(pathToFileURL(process.env.STAND_LIB).href)
const [container] = process.argv.slice(2)
const address = () => `http://${docker('port', container, '8899/tcp').split('\n')[0].trim()}`

// NOTE: A request through the HTTP proxy of the router: the rule matches by the name, so the
// target needs no DNS and no Internet; the answer itself does not matter.
function visit(host) {
  const [hostname, port] = docker('port', container, '7899/tcp').split('\n')[0].trim().split(':')
  return new Promise((resolve) => {
    const request = http.request({ hostname, port, path: `http://${host}/`, headers: { Host: host }, timeout: 5000 },
      (response) => {
        response.resume()
        response.on('end', () => resolve(response.statusCode))
      })
    request.on('error', () => resolve(0))
    request.on('timeout', () => request.destroy())
    request.end()
  })
}

async function visitAll(host, count) {
  for (let index = 0; index < count; index++) {
    await visit(host)
  }
}

async function hits(client) {
  return (await client.call('GET', '/api/routes')).hits
}

async function settle(client, key, count) {
  return waitFor(async () => ((await hits(client))[key]?.count || 0) >= count, 15)
}

async function applied(client, text) {
  return waitFor(async () => (await client.call('GET', '/api/router/config')).config.includes(text), 20)
}

async function run() {
  let client = await signIn(container)
  const one = await client.call('POST', '/api/routes', { id: 'One', directive: 'DOMAIN', target: 'DIRECT', enabled: true, params: ['one.test'] })
  const two = await client.call('POST', '/api/routes', { id: 'Two', directive: 'DOMAIN-SUFFIX', target: 'REJECT', enabled: true, params: ['two.test'] })
  check('routes are applied', Boolean(await applied(client, 'two.test')))

  await visitAll('one.test', 3)
  await visitAll('a.two.test', 2)
  await visitAll('other.test', 1)
  await settle(client, one.key, 3)
  await settle(client, two.key, 2)
  let counters = await hits(client)
  check('route One counts 3', counters[one.key]?.count === 3, JSON.stringify(counters[one.key]))
  check('route Two counts 2', counters[two.key]?.count === 2, JSON.stringify(counters[two.key]))
  check('Everything else counts the rest', (counters.default?.count || 0) >= 1, JSON.stringify(counters.default))
  check('last hit time is recent', Math.abs(Date.now() / 1000 - (counters[one.key]?.last || 0)) < 60)

  const sampled = await waitFor(async () => {
    const samples = (await client.call('GET', '/api/history')).samples
    const sum = samples.reduce((total, sample) => total + (sample.hits?.[one.key] || 0), 0)
    return sum === 3 ? samples : null
  }, 15)
  check('history samples hold the hits', Boolean(sampled))

  await client.call('POST', '/api/routes', { id: 'Three', directive: 'DOMAIN', target: 'DIRECT', enabled: true, params: ['three.test'] })
  check('reload is applied', Boolean(await applied(client, 'three.test')))
  await sleep(3000)
  counters = await hits(client)
  check('reload keeps the counts', counters[one.key]?.count === 3 && counters[two.key]?.count === 2, JSON.stringify(counters))
  await visitAll('one.test', 1)
  await settle(client, one.key, 4)
  await sleep(3000)
  check('a hit after the reload is counted once', (await hits(client))[one.key]?.count === 4)

  docker('restart', container)
  client = await signIn(container)
  check('counts survive a restart', (await hits(client))[one.key]?.count === 4)

  const browser = await puppeteer.launch({ executablePath: findBrowser(), headless: true,
    args: ['--no-sandbox', '--no-proxy-server'] })
  try {
    const page = await browser.newPage()
    await page.setViewport({ width: 1400, height: 1000 })
    await page.goto(address(), { waitUntil: 'networkidle0' })
    await page.evaluate(async (account) => {
      await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'stand' },
        body: JSON.stringify(account) })
    }, { username: 'admin', password: 'Stand-admin-1' })
    await visitAll('one.test', 2)
    await page.evaluate(() => localStorage.setItem('proximity-dashboard-tab', 'overview'))
    await page.goto(`${address()}/#/dashboard`, { waitUntil: 'networkidle0' })
    await page.reload({ waitUntil: 'networkidle0' })
    const dashboard = await waitFor(() => page.evaluate(() => {
      const card = document.querySelector('.dashboard-hits')
      const bars = card ? [...card.querySelectorAll('svg path')].filter((item) => item.getAttribute('d')).length : 0
      const legend = card ? card.querySelector('.chart-legend')?.innerText || '' : ''
      return bars > 0 && legend.includes('One') ? { bars, legend } : null
    }), 20)
    check('dashboard overview draws the route hits', Boolean(dashboard), JSON.stringify(dashboard))
    await page.goto(`${address()}/#/routes`, { waitUntil: 'networkidle0' })
    await page.reload({ waitUntil: 'networkidle0' })
    const chart = await waitFor(() => page.evaluate(() => {
      const card = document.querySelector('.hits-chart')
      const bars = card ? [...card.querySelectorAll('svg path')].filter((item) => item.getAttribute('d')).length : 0
      const legend = card ? card.querySelector('.chart-legend')?.innerText || '' : ''
      return bars > 0 && legend.includes('One') ? { bars, legend } : null
    }), 20)
    check('chart draws the hits', Boolean(chart), JSON.stringify(chart))
    const column = await page.evaluate(() => [...document.querySelectorAll('.ant-table-row')]
      .find((row) => row.innerText.includes('One'))?.innerText || '')
    check('Hits column shows the count', /\b6\b/.test(column), column.replace(/\s+/g, ' '))
  } finally {
    await browser.close()
  }

  await client.call('DELETE', '/api/routes/hits')
  check('reset empties the counters', Object.keys(await hits(client)).length === 0)
}

try {
  await run()
} catch (error) {
  check('stand run', false, error.stack)
}
finish()
