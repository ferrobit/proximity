import { accessSync, constants, statSync } from 'node:fs'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { PNG } from 'pngjs'
import puppeteer from 'puppeteer-core'

const shots = [
  { file: 'outbound-general', section: 'outbounds', name: 'Proxy DE', tab: 'General' },
  { file: 'outbound-socks5', section: 'outbounds', name: 'Proxy DE', tab: 'SOCKS5 Proxy' },
  { file: 'outbound-http', section: 'outbounds', name: 'Proxy US', tab: 'HTTP Proxy' },
  { file: 'outbound-host', section: 'outbounds', name: 'Home ISP', tab: 'HOST Network' },
  { file: 'outbound-openvpn', section: 'outbounds', name: 'Corp VPN', tab: 'OpenVPN' },
  { file: 'outbound-openvpn-auth', section: 'outbounds', name: 'Corp VPN', tab: 'Authentication' },
  { file: 'outbound-monitoring', section: 'outbounds', name: 'Corp VPN', tab: 'Monitoring' },
  { file: 'outbound-amneziawg', section: 'outbounds', name: 'AWG NL', tab: 'AmneziaWG' },
  { file: 'inbound-openvpn', section: 'inbounds', name: 'Office VPN', tab: 'Server' },
  { file: 'inbound-users', section: 'inbounds', name: 'Office VPN', tab: 'Users' }
]
// NOTE: Any browser on the Chromium engine fits. The names are looked up in PATH one by one,
// as "which" does, so the order here is the order of choice; the list of paths below is only
// a reserve for a browser that is installed but is not in PATH.
const browsers = [
  'google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser', 'ungoogled-chromium',
  'brave-browser', 'brave-browser-stable', 'brave',
  'thorium-browser',
  'microsoft-edge', 'microsoft-edge-stable',
  'vivaldi', 'vivaldi-stable',
  'yandex-browser', 'yandex-browser-stable', 'yandex-browser-corporate',
  'google-chrome-beta', 'google-chrome-unstable', 'brave-browser-beta', 'brave-browser-nightly',
  'microsoft-edge-beta', 'microsoft-edge-dev', 'vivaldi-snapshot', 'yandex-browser-beta',
  'iridium-browser'
]
const applications = [
  '/opt/google/chrome/google-chrome',
  '/snap/bin/chromium',
  '/opt/brave.com/brave/brave-browser',
  '/snap/bin/brave',
  '/opt/chromium.org/thorium/thorium-browser',
  '/opt/microsoft/msedge/microsoft-edge',
  '/opt/vivaldi/vivaldi',
  '/opt/yandex/browser/yandex-browser',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/Applications/Brave Browser.app/Contents/MacOS/Brave Browser',
  '/Applications/Thorium.app/Contents/MacOS/Thorium',
  '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
  '/Applications/Vivaldi.app/Contents/MacOS/Vivaldi',
  '/Applications/Yandex.app/Contents/MacOS/Yandex'
]
const folder = path.dirname(fileURLToPath(import.meta.url))
const settleTime = 900
const startTimeout = 90

function sleep(time) {
  return new Promise((resolve) => setTimeout(resolve, time))
}

function selectShots(names) {
  const known = shots.map((shot) => shot.file)
  const wanted = names.map((name) => name.replace(/\.png$/, ''))
  const unknown = wanted.filter((name) => !known.includes(name))
  if (unknown.length !== 0) {
    throw new Error(`unknown picture '${unknown.join("', '")}'; known pictures: ${known.join(', ')}`)
  }
  return wanted.length === 0 ? shots : shots.filter((shot) => wanted.includes(shot.file))
}

function describeOverride() {
  const command = process.env.MAKELEVEL ? 'make screenshots' : 'scripts/screenshots.sh'
  return [
    'to use another browser on the Chromium engine, give its name or its path:',
    `  CHROME=/path/to/browser ${command}`
  ]
}

function isProgram(file) {
  try {
    accessSync(file, constants.X_OK)
    return statSync(file).isFile()
  } catch {
    return false
  }
}

function which(name) {
  if (name.includes(path.sep)) return isProgram(name) ? name : ''
  const directories = (process.env.PATH || '').split(path.delimiter).filter(Boolean)
  return directories.map((directory) => path.join(directory, name)).find(isProgram) || ''
}

function findBrowser() {
  const wanted = process.env.CHROME
  if (wanted) {
    if (which(wanted)) return which(wanted)
    throw new Error([`browser '${wanted}' given in CHROME is not found or is not a program`,
      ...describeOverride()].join('\n'))
  }
  for (const name of browsers) {
    if (which(name)) return which(name)
  }
  const application = applications.find(isProgram)
  if (!application) {
    throw new Error(['a browser on the Chromium engine is not found',
      `looked in PATH for: ${browsers.join(', ')}`,
      `and at: ${applications.join(', ')}`, ...describeOverride()].join('\n'))
  }
  return application
}

async function waitForServer(base) {
  for (let attempt = 0; attempt < startTimeout; attempt++) {
    try {
      if ((await fetch(`${base}/api/session`)).ok) return
    } catch {
      // NOTE: The server is not listening yet.
    }
    await sleep(1000)
  }
  throw new Error(`Proximity at ${base} did not answer in ${startTimeout} seconds`)
}

async function call(page, method, target, body) {
  const result = await page.evaluate(async (method, target, body) => {
    const response = await fetch(target, {
      method,
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'screenshots' },
      body: body === undefined ? undefined : JSON.stringify(body)
    })
    return { status: response.status, text: await response.text() }
  }, method, target, body)
  if (result.status !== 200) throw new Error(`${method} ${target} failed (${result.status}): ${result.text}`)
  return result.text ? JSON.parse(result.text) : null
}

async function seed(page) {
  const data = JSON.parse(await readFile(path.join(folder, 'seed.json'), 'utf8'))
  for (const section of ['outbounds', 'inbounds']) {
    const present = (await call(page, 'GET', `/api/${section}`))[section].map((item) => item.id)
    for (const item of data[section]) {
      if (!present.includes(item.id)) await call(page, 'POST', `/api/${section}`, item)
    }
  }
}

function samePixels(left, right) {
  const first = PNG.sync.read(left)
  const second = PNG.sync.read(right)
  return first.width === second.width && first.height === second.height &&
    first.data.equals(second.data)
}

// NOTE: A file is written only when its pixels changed: the same picture encoded in another
// way (another browser version, added metadata) must not show up as a change in Git.
async function savePicture(file, picture) {
  let saved
  try {
    saved = await readFile(file)
  } catch {
    await writeFile(file, picture)
    return 'new'
  }
  try {
    if (samePixels(saved, picture)) return 'unchanged'
  } catch {
    // NOTE: The saved file is not a readable PNG; the new picture replaces it.
  }
  await writeFile(file, picture)
  return 'updated'
}

async function takeShot(page, base, output, shot) {
  await page.goto(`${base}/#/${shot.section}`, { waitUntil: 'networkidle0' })
  await page.reload({ waitUntil: 'networkidle0' })
  await page.waitForSelector('.ant-table-row')
  const opened = await page.evaluate((name) => {
    const rows = [...document.querySelectorAll('.ant-table-row')]
    const row = rows.find((item) => item.textContent.includes(name))
    const icon = row && row.querySelector('.anticon-edit')
    if (icon) icon.closest('button').click()
    return Boolean(icon)
  }, shot.name)
  if (!opened) throw new Error(`edit button of '${shot.name}' is not found`)
  await page.waitForSelector('.ant-modal-content .ant-tabs-tab')
  const labels = await page.evaluate((tab) => {
    const tabs = [...document.querySelectorAll('.ant-modal-content .ant-tabs-tab-btn')]
    const found = tabs.find((item) => item.textContent.trim() === tab)
    if (found) found.click()
    return tabs.map((item) => item.textContent.trim())
  }, shot.tab)
  if (!labels.includes(shot.tab)) {
    throw new Error(`tab '${shot.tab}' of '${shot.name}' is not found among: ${labels.join(', ')}`)
  }
  await sleep(settleTime)
  await page.mouse.move(5, 5)
  // NOTE: The page shows through the rounded corners of the dialog and its states change
  // from run to run; without the page the same dialog gives the same file.
  const hidden = await page.evaluate(() => {
    const application = document.querySelector('#app')
    const separate = Boolean(application) && !application.querySelector('.ant-modal-content')
    if (separate) application.style.visibility = 'hidden'
    return separate
  })
  if (!hidden) throw new Error('the page behind the dialog cannot be hidden')
  const dialog = await page.$('.ant-modal-content')
  const picture = Buffer.from(await dialog.screenshot({ type: 'png' }))
  const result = await savePicture(path.join(output, `${shot.file}.png`), picture)
  console.log(`screenshots: ${shot.file}.png: ${result}`)
  return result
}

async function run() {
  const [base, output, ...names] = process.argv.slice(2)
  if (base === '--browser') {
    console.log(findBrowser())
    return
  }
  if (!base || !output) throw new Error('usage: screenshots.mjs <url> <output folder> [picture ...]')
  const selected = selectShots(names)
  const options = ['--no-proxy-server', '--font-render-hinting=none']
  // NOTE: Chrome refuses to start its sandbox for root (containers, CI).
  if (process.getuid && process.getuid() === 0) options.push('--no-sandbox')
  await waitForServer(base)
  await mkdir(output, { recursive: true })
  const browser = await puppeteer.launch({ executablePath: findBrowser(), headless: true, args: options })
  try {
    const page = await browser.newPage()
    await page.setViewport({ width: 1440, height: 1300, deviceScaleFactor: 2 })
    await page.goto(`${base}/`, { waitUntil: 'networkidle0' })
    await call(page, 'POST', '/api/login', {
      username: process.env.SCREENSHOT_USER || 'admin',
      password: process.env.SCREENSHOT_PASSWORD || ''
    })
    await seed(page)
    const counts = { new: 0, updated: 0, unchanged: 0 }
    for (const shot of selected) counts[await takeShot(page, base, output, shot)]++
    console.log(`screenshots: ${counts.new} new, ${counts.updated} updated, ` +
      `${counts.unchanged} unchanged`)
  } finally {
    await browser.close()
  }
}

run().catch((error) => {
  for (const line of error.message.split('\n')) console.error(`screenshots: ${line}`)
  process.exit(1)
})
