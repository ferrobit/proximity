// Checks of the stand "logs": the button "Clear" of the log window and the setting that clears
// a log on Start and Restart.
// NOTE: check.sh copies this file next to the tools of the screenshots and runs it there.
import { pathToFileURL } from 'node:url'
import puppeteer from 'puppeteer-core'
import { findBrowser } from './browser.mjs'

const { account, check, docker, finish, logOf, signIn, sleep, stateOf, waitFor } = await import(pathToFileURL(process.env.STAND_LIB).href)
const [container] = process.argv.slice(2)
// NOTE: A restart of the container publishes the page on another port.
const address = () => `http://${docker('port', container, '8899/tcp').split('\n')[0].trim()}`
const stopped = 'stopped by user'

async function control(client, section, id, action) {
  await client.call('POST', `/api/${section}/${encodeURIComponent(id)}/${action}`)
  await sleep(1500)
  return logOf(client, section, id)
}

// NOTE: The window of the log opens from the button with the tooltip of the row, the button
// "Clear" asks for a confirmation in a popup.
async function clearInPage(page, section, id, tooltip) {
  await page.goto(`${address()}/#/${section}`, { waitUntil: 'networkidle0' })
  await page.reload({ waitUntil: 'networkidle0' })
  const opened = await page.evaluate((id, tooltip) => {
    const row = [...document.querySelectorAll('.ant-table-row')].find((item) => item.innerText.includes(id))
    const button = row && [...row.querySelectorAll('button')].find((item) => item.querySelector('.anticon-file-text'))
    if (button) button.click()
    return Boolean(button)
  }, id, tooltip)
  if (!opened) return { opened, lines: -1 }
  await page.waitForSelector('.log-view', { visible: true })
  await waitFor(() => page.evaluate(() => document.querySelectorAll('.log-row').length > 0), 10)
  const before = await page.evaluate(() => document.querySelectorAll('.log-row').length)
  const clicked = await page.evaluate(() => {
    const button = [...document.querySelectorAll('.ant-modal button')].find((item) => item.innerText.trim() === 'Clear')
    if (button) button.click()
    return Boolean(button)
  })
  await page.waitForSelector('.ant-popconfirm', { visible: true })
  await page.evaluate(() => [...document.querySelectorAll('.ant-popconfirm button')].find((item) => item.innerText.trim() === 'Clear').click())
  const empty = await waitFor(() => page.evaluate(() => document.querySelector('.log-empty') !== null &&
    document.querySelectorAll('.log-row').length === 0), 10)
  const toast = await page.evaluate(() => document.body.innerText.includes('Log cleared'))
  return { opened, clicked, before, empty: Boolean(empty), toast }
}

async function run() {
  const client = await signIn(container)
  await client.call('POST', '/api/outbounds', { type: 'host', enabled: true, autostart: true, statistics: true })
  await client.call('POST', '/api/inbounds', { type: 'socks5', enabled: true, autostart: true, anonymous: true, users: [] })
  check('services are up', Boolean(await waitFor(async () => (await stateOf(client, 'outbounds', 'HOST 1')).state === 'up' &&
    (await stateOf(client, 'inbounds', 'SOCKS5 1')).state === 'up', 60)))
  check('setting is on by default', (await client.call('GET', '/api/settings')).clear_log_on_restart === true)

  for (const [section, id] of [['outbounds', 'HOST 1'], ['inbounds', 'SOCKS5 1']]) {
    check(`${id}: Stop writes to the log`, (await control(client, section, id, 'stop')).includes(stopped))
    check(`${id}: Start clears the log`, !(await control(client, section, id, 'start')).includes(stopped))
    await control(client, section, id, 'stop')
    check(`${id}: Restart clears the log`, !(await control(client, section, id, 'restart')).includes(stopped))
    const lines = await logOf(client, section, id)
    check(`${id}: Start of a running service keeps the log`, (await control(client, section, id, 'start')) === lines)
  }

  const settings = await client.call('GET', '/api/settings')
  await client.call('PUT', '/api/settings', { ...settings, clear_log_on_restart: false })
  for (const [section, id] of [['outbounds', 'HOST 1'], ['inbounds', 'SOCKS5 1']]) {
    await control(client, section, id, 'stop')
    check(`${id}: setting off, Start keeps the log`, (await control(client, section, id, 'start')).includes(stopped))
    check(`${id}: setting off, Restart keeps the log`, (await control(client, section, id, 'restart')).includes(stopped))
  }

  docker('restart', container)
  const again = await signIn(container)
  await waitFor(async () => (await stateOf(again, 'outbounds', 'HOST 1')).state === 'up', 60)
  check('restart of the container keeps the logs', (await logOf(again, 'outbounds', 'HOST 1')).includes('shutting down') &&
    (await logOf(again, 'inbounds', 'SOCKS5 1')).includes('shutting down'))

  const options = ['--no-proxy-server']
  if (process.getuid && process.getuid() === 0) options.push('--no-sandbox')
  const browser = await puppeteer.launch({ executablePath: findBrowser(), headless: true, args: options })
  try {
    const page = await browser.newPage()
    await page.setViewport({ width: 1440, height: 900 })
    await page.goto(`${address()}/`, { waitUntil: 'networkidle0' })
    await page.evaluate(async (account) => {
      await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'stand' },
        body: JSON.stringify(account) })
    }, account)
    for (const [section, id, tooltip] of [['outbounds', 'HOST 1', 'Connection log'], ['inbounds', 'SOCKS5 1', 'Inbound log']]) {
      const result = await clearInPage(page, section, id, tooltip)
      check(`${id}: button "Clear" empties the log window`, result.opened && result.clicked && result.before > 0 && result.empty && result.toast,
        JSON.stringify(result))
      check(`${id}: the log is empty on the server`, (await logOf(again, section, id)) === '')
    }
    await page.goto(`${address()}/#/settings`, { waitUntil: 'networkidle0' })
    await page.reload({ waitUntil: 'networkidle0' })
    const label = 'Clear the log when a connection is started or restarted'
    const box = await page.evaluate((label) => {
      const item = [...document.querySelectorAll('.ant-checkbox-wrapper')].find((element) => element.innerText.includes(label))
      return item ? item.querySelector('input').checked : null
    }, label)
    check('Settings show the checkbox with the saved value (off)', box === false)
  } finally {
    await browser.close()
  }

  docker('restart', container)
  const last = await signIn(container)
  await waitFor(async () => (await stateOf(last, 'outbounds', 'HOST 1')).state === 'up', 60)
  const after = (await logOf(last, 'outbounds', 'HOST 1')).split('\n')
  check('cleared log stays cleared after a restart of the container', !after.some((line) => line.includes(stopped)), `${after.length} lines`)
  finish()
}

run().catch((error) => {
  console.error(`FAIL  ${error.message}`)
  process.exit(1)
})
