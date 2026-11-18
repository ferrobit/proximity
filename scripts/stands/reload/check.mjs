// Checks of the stand "reload": an open page notices a restart of Proximity by the instance
// mark of the answers and reloads itself; with an open dialog it shows a notice and waits.
// NOTE: check.sh copies this file next to the tools of the screenshots and runs it there.
import { pathToFileURL } from 'node:url'
import puppeteer from 'puppeteer-core'
import { findBrowser } from './browser.mjs'

const { check, docker, finish, sleep, waitFor } = await import(pathToFileURL(process.env.STAND_LIB).href)
const [container, address] = process.argv.slice(2)
const notice = 'Proximity was restarted or updated'

// NOTE: A mark in the page is lost when the page is loaded again.
async function marked(page) {
  try {
    return await page.evaluate(() => window.standMark === true)
  } catch (error) {
    return true
  }
}

async function mark(page) {
  await page.waitForSelector('.brand-name', { timeout: 60000 })
  await page.evaluate(() => { window.standMark = true })
}

const shows = (page, text) => page.evaluate((part) => document.body.innerText.includes(part), text).catch(() => false)

async function run() {
  const options = ['--no-proxy-server']
  if (process.getuid && process.getuid() === 0) options.push('--no-sandbox')
  const browser = await puppeteer.launch({ executablePath: findBrowser(), headless: true, args: options })
  try {
    const page = await browser.newPage()
    await page.setViewport({ width: 1280, height: 800 })
    await page.goto(address, { waitUntil: 'networkidle2' })
    await mark(page)
    await sleep(4000)
    check('page stays while the same instance answers', await marked(page))

    docker('restart', container)
    check('page reloads itself after a restart', Boolean(await waitFor(async () => !(await marked(page)), 60)))
    await mark(page)

    const [button] = await page.$$('xpath/.//button[contains(., "Sign in")]')
    await button.click()
    await page.waitForSelector('.ant-modal-wrap input', { visible: true })
    await page.type('.ant-modal-wrap input', 'unsaved')
    docker('restart', container)
    check('notice appears while a dialog is open', Boolean(await waitFor(() => shows(page, notice), 60)))
    await sleep(5000)
    check('page with an open dialog is not reloaded', await marked(page) &&
      await page.$eval('.ant-modal-wrap input', (input) => input.value) === 'unsaved')
    check('notice has the button "Reload now"', await shows(page, 'Reload now'))
    await page.click('.ant-modal-close')
    check('page reloads itself when the dialog is closed', Boolean(await waitFor(async () => !(await marked(page)), 20)))
    await mark(page)
    check('no notice after the reload', !(await shows(page, notice)))
  } finally {
    await browser.close()
  }
  finish()
}

run().catch((error) => {
  console.error(`FAIL  ${error.message}`)
  process.exit(1)
})
