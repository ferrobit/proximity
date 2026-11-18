import { mkdir } from 'node:fs/promises'
import path from 'node:path'
import puppeteer from 'puppeteer-core'
import { findBrowser, savePicture } from './browser.mjs'
import { Client, account, readSeed, seedMain, seedPeer, sleep, startTraffic, waitForLinks } from './stand.mjs'

// NOTE: A static picture depends only on the code and on seed.json and gives the same pixels
// in every run. A live picture shows states, traffic, time or logs and differs every time.
const shots = [
  { file: 'setup', kind: 'setup', live: true },
  { file: 'outbound-general', kind: 'tab', section: 'outbounds', name: 'Proxy DE', tab: 'General' },
  { file: 'outbound-socks5', kind: 'tab', section: 'outbounds', name: 'Proxy DE', tab: 'SOCKS5 Proxy' },
  { file: 'outbound-dns', kind: 'tab', section: 'outbounds', name: 'Proxy DE', tab: 'DNS' },
  { file: 'outbound-http', kind: 'tab', section: 'outbounds', name: 'Proxy US', tab: 'HTTP Proxy' },
  { file: 'outbound-host', kind: 'tab', section: 'outbounds', name: 'Home ISP', tab: 'HOST Network' },
  { file: 'outbound-openvpn', kind: 'tab', section: 'outbounds', name: 'Corp VPN', tab: 'OpenVPN' },
  { file: 'outbound-openvpn-auth', kind: 'tab', section: 'outbounds', name: 'Corp VPN', tab: 'Authentication' },
  { file: 'outbound-monitoring', kind: 'tab', section: 'outbounds', name: 'Corp VPN', tab: 'Monitoring' },
  { file: 'outbound-amneziawg', kind: 'tab', section: 'outbounds', name: 'AWG NL', tab: 'AmneziaWG' },
  { file: 'inbound-openvpn', kind: 'tab', section: 'inbounds', name: 'Office VPN', tab: 'Server' },
  { file: 'inbound-users', kind: 'tab', section: 'inbounds', name: 'Office VPN', tab: 'Users' },
  { file: 'inbound-export', kind: 'export', name: 'Office VPN', user: 'alice' },
  { file: 'route-dialog', kind: 'route', name: 'Corporate' },
  { file: 'route-editor-ip', kind: 'route', name: 'Office networks' },
  { file: 'fallback-dialog', kind: 'fallback', name: 'Streaming' },
  { file: 'router-config', kind: 'config', line: 'listeners:' },
  { file: 'dashboard', kind: 'dashboard', live: true },
  { file: 'throughput', kind: 'chart', live: true },
  { file: 'connection-map', kind: 'map', live: true },
  { file: 'connection-map-popover', kind: 'popover', name: 'Corp VPN', live: true },
  { file: 'outbounds', kind: 'page', section: 'outbounds', live: true },
  { file: 'outbound-add-menu', kind: 'menu', live: true },
  { file: 'outbound-log', kind: 'log', name: 'Corp VPN', live: true },
  { file: 'routes', kind: 'page', section: 'routes', live: true },
  { file: 'fallbacks', kind: 'element', section: 'routes', selector: '.fallbacks', live: true },
  { file: 'inbounds', kind: 'element', section: 'inbounds', selector: '.content > div', padding: 24, live: true },
  { file: 'settings', kind: 'settings', live: true },
  { file: 'dashboard-dark', kind: 'dashboard', dark: true, live: true },
  { file: 'outbounds-dark', kind: 'page', section: 'outbounds', dark: true, live: true },
  { file: 'connection-map-dark', kind: 'map', dark: true, live: true }
]
const groups = { static: (shot) => !shot.live, live: (shot) => shot.live }
const settleTime = 900
const stableTime = 300
const stableAttempts = 6
const defaultTraffic = 300
const pageHeight = 900
const dialogHeight = 1300

function selectShots(names) {
  const known = shots.map((shot) => shot.file)
  const wanted = names.map((name) => name.replace(/\.png$/, ''))
  const unknown = wanted.filter((name) => !known.includes(name) && !groups[name])
  if (unknown.length !== 0) {
    throw new Error(`unknown picture '${unknown.join("', '")}'\nknown pictures: ${known.join(', ')}\n` +
      `groups: ${Object.keys(groups).join(', ')}`)
  }
  return wanted.length === 0 ? shots : shots.filter((shot) => wanted.includes(shot.file) ||
    wanted.some((name) => groups[name] && groups[name](shot)))
}

async function openSection(stage, section, dark, height) {
  const { page, base } = stage
  await page.setViewport({ width: 1440, height: height || pageHeight, deviceScaleFactor: 2 })
  await page.evaluate((theme) => localStorage.setItem('proximity-theme', theme), dark ? 'dark' : 'light')
  await page.goto(`${base}/#/${section}`, { waitUntil: 'networkidle0' })
  await page.reload({ waitUntil: 'networkidle0' })
  await page.mouse.move(5, 5)
}

async function clickText(page, selector, text) {
  const clicked = await page.evaluate((selector, text) => {
    const items = [...document.querySelectorAll(selector)]
    const found = items.find((item) => item.textContent.trim() === text)
    if (found) found.click()
    return Boolean(found)
  }, selector, text)
  if (!clicked) throw new Error(`'${text}' is not found among '${selector}'`)
}

async function clickRowIcon(page, name, icon) {
  await page.waitForSelector('.ant-table-row')
  const clicked = await page.evaluate((name, icon) => {
    const rows = [...document.querySelectorAll('.ant-table-row')]
    const row = rows.find((item) => item.querySelector(icon) && (item.textContent.includes(name) ||
      [...item.querySelectorAll('input')].some((input) => input.value === name)))
    if (row) row.querySelector(icon).closest('button').click()
    return Boolean(row)
  }, name, icon)
  if (!clicked) throw new Error(`button '${icon}' of '${name}' is not found`)
}

async function openDialog(stage, section, name, icon, height) {
  await openSection(stage, section, false, height || dialogHeight)
  await clickRowIcon(stage.page, name, icon || '.anticon-edit')
  await stage.page.waitForSelector('.ant-modal-content')
}

// NOTE: The page shows through the rounded corners of the dialog and its states change
// from run to run; without the page the same dialog gives the same file.
async function shootDialog(stage) {
  const { page } = stage
  await sleep(settleTime)
  await page.mouse.move(5, 5)
  const hidden = await page.evaluate(() => {
    const application = document.querySelector('#app')
    const dialogs = [...document.querySelectorAll('.ant-modal-wrap')].filter((item) => item.offsetParent !== null ||
      getComputedStyle(item).display !== 'none')
    if (!application || application.querySelector('.ant-modal-content') || dialogs.length === 0) return false
    application.style.visibility = 'hidden'
    dialogs.slice(0, -1).forEach((item) => { item.style.visibility = 'hidden' })
    dialogs[dialogs.length - 1].setAttribute('data-shot', 'dialog')
    return true
  })
  if (!hidden) throw new Error('the page behind the dialog cannot be hidden')
  // NOTE: An animation that is still running gives a different picture; the dialog is
  // taken until two pictures in a row are the same.
  const dialog = await page.$('[data-shot="dialog"] .ant-modal-content')
  let picture = Buffer.from(await dialog.screenshot({ type: 'png' }))
  for (let attempt = 0; attempt < stableAttempts; attempt++) {
    await sleep(stableTime)
    const next = Buffer.from(await dialog.screenshot({ type: 'png' }))
    if (next.equals(picture)) break
    picture = next
  }
  return picture
}

async function shootElement(stage, selector, padding) {
  const { page } = stage
  await sleep(settleTime)
  const element = await page.$(selector)
  if (!element) throw new Error(`element '${selector}' is not found`)
  if (!padding) return element.screenshot({ type: 'png' })
  const box = await element.boundingBox()
  return page.screenshot({ type: 'png', clip: { x: box.x - padding, y: box.y - padding,
    width: box.width + 2 * padding, height: box.height + 2 * padding } })
}

async function openMap(stage, dark) {
  await openSection(stage, 'dashboard', dark)
  await clickText(stage.page, '.dashboard-tabs .ant-tabs-tab-btn', 'Connection map')
  await stage.page.waitForSelector('.map-card .node')
}

const takers = {
  async setup(stage) {
    if (!stage.fresh) throw new Error('the picture of the first run needs an instance without users')
    return stage.setupPicture
  },
  async tab(stage, shot) {
    await openDialog(stage, shot.section, shot.name)
    await stage.page.waitForSelector('.ant-modal-content .ant-tabs-tab')
    await clickText(stage.page, '.ant-modal-content .ant-tabs-tab-btn', shot.tab)
    return shootDialog(stage)
  },
  async export(stage, shot) {
    await openDialog(stage, 'inbounds', shot.name)
    await clickText(stage.page, '.ant-modal-content .ant-tabs-tab-btn', 'Users')
    await sleep(settleTime)
    await clickRowIcon(stage.page, shot.user, '.ant-modal-content .anticon-export')
    await stage.page.waitForFunction(() => document.querySelectorAll('.ant-modal-content').length === 2)
    return shootDialog(stage)
  },
  async route(stage, shot) {
    await openDialog(stage, 'routes', shot.name)
    await stage.page.waitForSelector('.rule-collapse .ant-collapse-header')
    await stage.page.click('.rule-collapse .ant-collapse-header')
    return shootDialog(stage)
  },
  async fallback(stage, shot) {
    await openSection(stage, 'routes', false, dialogHeight)
    const clicked = await stage.page.evaluate((name) => {
      const rows = [...document.querySelectorAll('.fallbacks .ant-table-row')]
      const row = rows.find((item) => item.textContent.includes(name))
      const found = row && row.querySelector('.anticon-edit')
      if (found) found.closest('button').click()
      return Boolean(found)
    }, shot.name)
    if (!clicked) throw new Error(`fallback '${shot.name}' is not found`)
    await stage.page.waitForSelector('.ant-modal-content')
    return shootDialog(stage)
  },
  async config(stage, shot) {
    await openSection(stage, 'routes', false, dialogHeight)
    await clickText(stage.page, '.toolbar button', 'Router configuration')
    await stage.page.waitForSelector('.ant-modal-content .config-row')
    await clickText(stage.page, '.config-format .ant-radio-button-wrapper', 'YAML')
    await sleep(settleTime)
    await stage.page.evaluate((line) => {
      const rows = [...document.querySelectorAll('.config-row')]
      const row = rows.find((item) => item.querySelector('.config-text').textContent === line)
      const body = document.querySelector('.config-body')
      if (row) body.scrollTop = row.offsetTop - body.offsetTop - row.offsetHeight
      document.activeElement.blur()
    }, shot.line)
    return shootDialog(stage)
  },
  async dashboard(stage, shot) {
    await openSection(stage, 'dashboard', shot.dark)
    await clickText(stage.page, '.dashboard-tabs .ant-tabs-tab-btn', 'Overview')
    await clickText(stage.page, '.traffic-chart .ant-radio-button-wrapper', '5m')
    await stage.page.evaluate(() => document.activeElement.blur())
    await sleep(settleTime)
    return stage.page.screenshot({ type: 'png' })
  },
  async chart(stage) {
    await takers.dashboard(stage, {})
    const box = await (await stage.page.$('.traffic-chart .chart-box')).boundingBox()
    await stage.page.mouse.move(box.x + box.width * 0.78, box.y + box.height * 0.45)
    return shootElement(stage, '.traffic-chart')
  },
  async map(stage, shot) {
    await openMap(stage, shot.dark)
    return shootElement(stage, '.map-card')
  },
  async popover(stage, shot) {
    await openMap(stage)
    const node = await stage.page.evaluateHandle((name) => {
      const nodes = [...document.querySelectorAll('.map-card .node')]
      return nodes.find((item) => item.querySelector('.node-title').textContent.trim() === name)
    }, shot.name)
    const box = await node.asElement().boundingBox()
    await stage.page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await stage.page.waitForSelector('.popover-body', { visible: true })
    await sleep(settleTime)
    const card = await (await stage.page.$('.map-card')).boundingBox()
    const left = Math.max(card.x, box.x - 280)
    const right = Math.min(card.x + card.width, box.x + box.width + 250)
    const top = card.y
    return stage.page.screenshot({ type: 'png',
      clip: { x: left, y: top, width: right - left, height: Math.min(card.height, box.y + 330 - top) } })
  },
  async page(stage, shot) {
    await openSection(stage, shot.section, shot.dark)
    await stage.page.waitForSelector('.ant-table-row')
    await sleep(settleTime)
    return stage.page.screenshot({ type: 'png' })
  },
  async menu(stage) {
    await openSection(stage, 'outbounds')
    await stage.page.waitForSelector('.ant-table-row')
    const button = await stage.page.evaluateHandle(() => {
      const buttons = [...document.querySelectorAll('.toolbar button')]
      return buttons.find((item) => item.textContent.includes('Add outbound'))
    })
    await button.asElement().hover()
    await stage.page.waitForSelector('.ant-dropdown-menu', { visible: true })
    await sleep(settleTime)
    return stage.page.screenshot({ type: 'png', clip: { x: 200, y: 60, width: 700, height: 420 } })
  },
  async log(stage, shot) {
    await openDialog(stage, 'outbounds', shot.name, '.anticon-file-text', pageHeight)
    await stage.page.waitForSelector('.ant-modal-content .log-message')
    return shootDialog(stage)
  },
  async element(stage, shot) {
    await openSection(stage, shot.section, shot.dark)
    await stage.page.waitForSelector('.ant-table-row')
    return shootElement(stage, shot.selector, shot.padding)
  },
  async settings(stage) {
    await openSection(stage, 'settings')
    await stage.page.waitForSelector('.settings-card')
    await sleep(settleTime)
    // NOTE: The window is as tall as the page, so the fixed parts of the layout stay in place.
    const height = await stage.page.evaluate(() => {
      const content = document.querySelector('.content')
      return Math.ceil(content.getBoundingClientRect().top + content.scrollHeight) + 48
    })
    await stage.page.setViewport({ width: 1440, height, deviceScaleFactor: 2 })
    await sleep(settleTime)
    return stage.page.screenshot({ type: 'png' })
  }
}

async function prepareStand(stage, selected) {
  const { page, base, main, peer } = stage
  const seed = await readSeed()
  await page.goto(`${base}/`, { waitUntil: 'networkidle0' })
  stage.fresh = (await main.call('GET', '/api/session')).setup
  if (stage.fresh && selected.some((shot) => shot.kind === 'setup')) {
    await page.waitForSelector('.ant-modal-content')
    await sleep(settleTime)
    stage.setupPicture = await page.screenshot({ type: 'png' })
  }
  await seedMain(main, seed, await seedPeer(peer, seed))
  await page.evaluate(async (account) => {
    await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'screenshots' },
      body: JSON.stringify(account)
    })
  }, account)
  await waitForLinks(main, seed)
  if (selected.some((shot) => shot.live && shot.kind !== 'setup')) {
    stage.traffic = await startTraffic(peer, seed, stage.proxy, stage.socks)
  }
}

async function waitForTraffic(stage) {
  const wanted = Number(process.env.SCREENSHOT_TRAFFIC || defaultTraffic) * 1000
  const left = stage.traffic.started + wanted - Date.now()
  if (left > 0) {
    console.log(`screenshots: live pictures need ${Math.round(wanted / 1000)} seconds of traffic, ` +
      `${Math.round(left / 1000)} seconds left`)
    await sleep(left)
  }
}

async function run() {
  const [base, output, ...names] = process.argv.slice(2)
  if (base === '--browser') {
    console.log(findBrowser())
    return
  } else if (base === '--hosts') {
    console.log((await readSeed()).hosts.join('\n'))
    return
  }
  if (!base || !output) throw new Error('usage: screenshots.mjs <url> <output folder> [picture ...]')
  const selected = selectShots(names)
  const options = ['--no-proxy-server', '--font-render-hinting=none']
  // NOTE: Chrome refuses to start its sandbox for root (containers, CI).
  if (process.getuid && process.getuid() === 0) options.push('--no-sandbox')
  const stage = {
    base, output, main: new Client(base), peer: new Client(process.env.SCREENSHOT_PEER || ''),
    proxy: process.env.SCREENSHOT_PROXY || '', socks: process.env.SCREENSHOT_SOCKS || ''
  }
  await stage.main.waitForServer()
  await stage.peer.waitForServer()
  await mkdir(output, { recursive: true })
  const browser = await puppeteer.launch({ executablePath: findBrowser(), headless: true, args: options })
  try {
    stage.page = await browser.newPage()
    await stage.page.setViewport({ width: 1440, height: pageHeight, deviceScaleFactor: 2 })
    await prepareStand(stage, selected)
    const counts = { new: 0, updated: 0, unchanged: 0 }
    let waited = false
    for (const shot of [...selected.filter((item) => !item.live), ...selected.filter((item) => item.live)]) {
      if (stage.traffic && shot.live && !waited) {
        await waitForTraffic(stage)
        waited = true
      }
      const picture = Buffer.from(await takers[shot.kind](stage, shot))
      const result = await savePicture(path.join(output, `${shot.file}.png`), picture)
      console.log(`screenshots: ${shot.file}.png: ${result}`)
      counts[result]++
    }
    console.log(`screenshots: ${counts.new} new, ${counts.updated} updated, ` +
      `${counts.unchanged} unchanged`)
  } finally {
    if (stage.traffic) stage.traffic.stopped = true
    await browser.close()
  }
}

run().then(() => process.exit(0)).catch((error) => {
  for (const line of error.message.split('\n')) console.error(`screenshots: ${line}`)
  process.exit(1)
})
