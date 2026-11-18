import { spawnSync } from 'node:child_process'
import { Client, sleep } from '../screenshots/stand.mjs'

export { sleep }

export const account = { username: 'admin', password: 'Stand-admin-1' }

const results = []

// NOTE: The output and the errors of the command come back together: "docker logs" writes
// the log of a container to both.
export function docker(...parameters) {
  const result = spawnSync('docker', parameters, { encoding: 'utf8' })
  return `${result.stdout || ''}${result.stderr || ''}`
}

export async function signIn(container) {
  const address = docker('port', container, '8899/tcp').split('\n')[0].trim()
  const client = new Client(`http://${address}`)
  await client.waitForServer()
  await client.call('POST', '/api/login', account)
  return client
}

export function check(name, passed, detail) {
  results.push(passed)
  console.log(`${passed ? 'PASS' : 'FAIL'}  ${name}${detail ? `: ${detail}` : ''}`)
}

export async function waitFor(test, timeout) {
  for (const started = Date.now(); Date.now() - started < timeout * 1000;) {
    const value = await test()
    if (value) return value
    await sleep(500)
  }
  return null
}

export async function stateOf(client, section, id) {
  const item = (await client.call('GET', `/api/${section}`))[section].find((entry) => entry.id === id)
  return item ? item.status : { state: 'missing', message: '' }
}

export async function logOf(client, section, id) {
  const result = await client.call('GET', `/api/${section}/${encodeURIComponent(id)}/log`)
  return (result.lines || result).join('\n')
}

export function finish() {
  const failed = results.filter((passed) => !passed).length
  console.log(`${results.length - failed} of ${results.length} checks passed`)
  process.exit(failed === 0 ? 0 : 1)
}
