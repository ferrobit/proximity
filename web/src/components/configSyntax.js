import { highlightLine, highlightRule } from './ruleSyntax'

const linePattern = /^(\s*)(?:"((?:[^"\\]|\\.)*)"(\s*:\s*))?(.*?)(,?)$/
const stringPattern = /^"((?:[^"\\]|\\.)*)"$/
const numberPattern = /^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?$/
const portPattern = /^\d+(?:-\d+)?$/
const domainKeys = ['nameserver-policy', 'hosts']
const targetKeys = ['name', 'proxy', 'dialer-proxy']
const targetArrays = ['proxies', 'use']
const secretKeys = ['password', 'private-key', 'secret', 'token', 'pre-shared-key', 'psk', 'auth-str', 'uuid']
const secretPattern = new RegExp(`^(\\s*"(?:${secretKeys.join('|')})"\\s*:\\s*)"(?:[^"\\\\]|\\\\.)*"`, 'gm')

export const hiddenValue = '<hidden>'

const token = (text, kind) => ({ text, kind })

function clean(tokens) {
  return tokens.length && !tokens.some((part) => part.kind === 'error') ? tokens : null
}

function scalar(text) {
  if (portPattern.test(text)) return [token(text, 'number')]
  const address = clean(highlightLine(text, 'IP-CIDR'))
  if (address) return address
  const hash = text.indexOf('#')
  if (hash > 0) return [...scalar(text.slice(0, hash)), token('#', 'punctuation'), token(text.slice(hash + 1), 'target')]
  if (/^[^\s/]+\.[^\s/]+$/.test(text) || /^[^\s/]+:\d{1,5}$/.test(text)) {
    const location = clean(highlightLine(text, 'DOMAIN-SUFFIX'))
    if (location) return location
  }
  return [token(text, 'string')]
}

function quoted(inner, parts) {
  return [token('"', 'punctuation'), ...parts.filter((part) => part.text), token('"', 'punctuation')]
}

function value(text, key, parent) {
  const found = text.match(stringPattern)
  if (found) {
    if (parent === 'rules' && !key) return quoted(found[1], highlightRule(found[1]))
    if (secretKeys.includes(key)) return quoted(found[1], [token(found[1], 'secret')])
    if (targetKeys.includes(key) || (!key && targetArrays.includes(parent))) return quoted(found[1], [token(found[1], 'target')])
    return quoted(found[1], scalar(found[1]))
  }
  if (numberPattern.test(text)) return [token(text, 'number')]
  if (/^(true|false|null)$/.test(text)) return [token(text, 'keyword')]
  if (/^[[\]{}]+$/.test(text)) return [token(text, 'punctuation')]
  return [token(text, 'plain')]
}

function name(text, parent) {
  if (domainKeys.includes(parent)) return quoted(text, scalar(text))
  return [token('"', 'punctuation'), token(text, 'key'), token('"', 'punctuation')]
}

export function hideSecrets(text) {
  let count = 0
  const result = text.replace(secretPattern, (match, prefix) => {
    count++
    return `${prefix}"${hiddenValue}"`
  })
  return { text: result, count }
}

export function highlightConfig(text) {
  const stack = []
  return text.replace(/\n$/, '').split('\n').map((line) => {
    const found = line.match(linePattern)
    if (!found) return [token(line, 'plain')]
    const [, indent, key, separator, body, comma] = found
    if (/^[}\]]/.test(body)) stack.pop()
    const parent = stack.length ? stack[stack.length - 1] : ''
    const result = []
    if (indent) result.push(token(indent, 'plain'))
    if (key !== undefined) result.push(...name(key, parent), token(separator, 'punctuation'))
    result.push(...value(body, key, parent))
    if (comma) result.push(token(comma, 'punctuation'))
    if (/[[{]$/.test(body)) stack.push(key !== undefined ? key : parent)
    return result
  })
}
