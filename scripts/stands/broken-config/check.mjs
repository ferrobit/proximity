// Checks the start with a partly invalid configuration on the stand "broken-config".
import { check, docker, finish, signIn } from '../lib.mjs'

const container = 'proximity-stand-broken'

async function run() {
  const client = await signIn(container)
  const problems = await client.call('GET', '/api/problems')
  const found = (kind, id, kept) => problems.items.some((item) => item.kind === kind && item.id === id && item.kept === kept)
  check('instance starts and answers', true)
  check('outbound that cannot be read is dropped', found('outbound', 'Broken proxy', false))
  check('outbound with a wrong value is dropped', found('outbound', 'No server', false))
  check('second outbound with the same ID is dropped', problems.items.filter((item) => item.id === 'Home').length === 1)
  check('fallback of a dropped outbound is dropped', found('fallback', 'Group', false))
  check('route with a wrong directive is dropped', found('route', 'Bad directive', false))
  check('inbound with a wrong port is dropped', found('inbound', 'Bad port', false))
  check('default target of a dropped outbound is reset', found('settings', 'default target', false))
  check('route to a dropped outbound is kept and reported', found('route', 'Through proxy', true))
  const outbounds = (await client.call('GET', '/api/outbounds')).outbounds.map((item) => item.id)
  const routes = (await client.call('GET', '/api/routes'))
  const inbounds = (await client.call('GET', '/api/inbounds')).inbounds.map((item) => item.id)
  check('valid outbound works', outbounds.join() === 'Home', outbounds.join())
  check('valid routes are kept', routes.routes.map((item) => item.id).join() === 'Through proxy,Good' && routes.default_target === 'DIRECT',
    `${routes.routes.map((item) => item.id).join()}; default ${routes.default_target}`)
  check('valid inbound is kept', inbounds.join() === 'Good', inbounds.join())
  const files = docker('exec', container, 'ls', '/config')
  check('damaged file is kept', files.includes(problems.backup), problems.backup)
  const original = docker('exec', container, 'cat', `/config/${problems.backup}`)
  check('copy holds the original text', original.includes('"not a number"'))
  check('start log names the problems', docker('logs', container).includes("configuration problem: outbound 'Broken proxy'"))
  docker('restart', container)
  const again = await signIn(container)
  check('report survives a restart', (await again.call('GET', '/api/problems')).items.length === problems.items.length)
  await again.call('DELETE', '/api/problems')
  check('administrator can dismiss the report', (await again.call('GET', '/api/problems')).items.length === 0)
  finish()
}

run().catch((error) => {
  console.error(`FAIL  ${error.message}`)
  process.exit(1)
})
