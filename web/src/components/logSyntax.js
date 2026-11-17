const stampPattern = /^(\d{4}-\d{2}-\d{2})([ T])(\d{2}:\d{2}:\d{2}(?:[.,]\d+)?Z?)(\s+)/
const levelPattern = /\b(?:level=)?(panic|fatal|critical|error|err|warning|warn|info|notice|debug|trace)\b|\[(PANIC|FATAL|ERROR|ERR|WARNING|WARN|INFO|NOTICE|DEBUG|TRACE)\]/i
const levels = { panic: 'error', fatal: 'error', critical: 'error', error: 'error', err: 'error', warning: 'warning', warn: 'warning',
  info: 'info', notice: 'info', debug: 'debug', trace: 'debug' }
const errorPattern = /\b(error|errors|fail|failed|failure|fatal|panic|denied|refused|unreachable|timed out|timeout|cannot|can't|unable to|invalid|bad state|aborted|AUTH_FAILED|no such host)\b/i
const warningPattern = /(?<![-_])\b(warning|warn|retry|retrying|reconnect|reconnecting|restart|restarting|deprecated|missing|stopped)\b/i
const notePattern = /^\s*note\b/i
const infoPattern = /^(starting|stopping|stopped|restart requested|start requested)\b/i
const successPattern = /\b(Initialization Sequence Completed|Peer Connection Initiated|connected|established|started|succeeded|is up|link up|recovered|ready)\b|^VERIFY\b.*\bOK\b/i
const debugPattern = /^\s*(?:[a-z0-9]+_[a-z0-9_]+\b|MULTI:|TLS:|Socket Buffers|Timers:|DCO version|library versions)/
const inlinePattern = /(https?:\/\/[^\s<>"'`]+)|('[^']*'|"[^"]*")|(\b\d{1,3}(?:\.\d{1,3}){3}(?:\/\d{1,2})?(?::\d{1,5})?\b)|(\b(?:level=)?(?:panic|fatal|critical|error|warning|warn|info|notice|debug|trace)\b|\[(?:PANIC|FATAL|ERROR|ERR|WARNING|WARN|INFO|NOTICE|DEBUG|TRACE)\])/gi

const token = (text, kind, extra) => ({ text, kind, ...extra })

export function severity(message) {
  const level = message.match(levelPattern)
  if (level) return levels[(level[1] || level[2]).toLowerCase()]
  if (notePattern.test(message)) return 'warning'
  if (infoPattern.test(message)) return 'info'
  if (errorPattern.test(message)) return 'error'
  if (successPattern.test(message)) return 'success'
  if (debugPattern.test(message)) return 'debug'
  if (warningPattern.test(message)) return 'warning'
  return 'plain'
}

function inline(message) {
  const result = []
  let last = 0
  for (const found of message.matchAll(inlinePattern)) {
    let [text] = found
    const kind = found[1] ? 'url' : found[2] ? 'quoted' : found[3] ? 'address' : 'level'
    if (kind === 'url') {
      text = text.replace(/[.,;:!?)\]}]+$/, '')
    }
    if (found.index > last) result.push(token(message.slice(last, found.index), 'text'))
    if (kind === 'quoted') {
      result.push(token(text[0], 'quoted'))
      result.push(...inline(text.slice(1, -1)).map((part) => part.kind === 'text' ? { ...part, kind: 'quoted' } : part))
      result.push(token(text[text.length - 1], 'quoted'))
    } else if (kind === 'level') {
      const name = text.replace(/^level=|[[\]]/gi, '').toLowerCase()
      result.push(token(text, 'level', { level: levels[name] || 'info' }))
    } else {
      result.push(token(text, kind, kind === 'url' ? { href: text } : {}))
    }
    last = found.index + text.length
  }
  if (last < message.length) result.push(token(message.slice(last), 'text'))
  return result
}

export function parseLogLine(line) {
  const stamp = line.match(stampPattern)
  const message = stamp ? line.slice(stamp[0].length) : line
  return {
    date: stamp ? stamp[1] : '',
    time: stamp ? stamp[3] : '',
    severity: severity(message),
    parts: inline(message)
  }
}
