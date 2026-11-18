import { accessSync, constants, statSync } from 'node:fs'
import { readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { PNG } from 'pngjs'

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

export function findBrowser() {
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

function samePixels(left, right) {
  const first = PNG.sync.read(left)
  const second = PNG.sync.read(right)
  return first.width === second.width && first.height === second.height &&
    first.data.equals(second.data)
}

// NOTE: A file is written only when its pixels changed: the same picture encoded in another
// way (another browser version, added metadata) must not show up as a change in Git.
export async function savePicture(file, picture) {
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
