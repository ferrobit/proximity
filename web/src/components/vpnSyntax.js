import { scalar } from './configSyntax'

const keywords = new Set(['udp', 'tcp', 'udp4', 'udp6', 'tcp4', 'tcp6', 'tcp-client', 'tcp-server', 'tun', 'tap', 'client',
  'server', 'net30', 'p2p', 'subnet', 'none', 'nointeract', 'interact', 'def1', 'bypass-dhcp', 'bypass-dns', 'block-local',
  'local', 'auto', 'adaptive', 'yes', 'no', 'via-file', 'via-env', 'exit-event', 'infinite', 'name', 'name-prefix',
  'subject', 'net_gateway', 'vpn_gateway', 'remote_host', 'default'])
const maskDirectives = ['route', 'ifconfig', 'server', 'ifconfig-push', 'push-reset']
const secretBlocks = ['key', 'tls-crypt', 'tls-auth', 'tls-crypt-v2', 'secret', 'pkcs12']
const secretKeys = ['privatekey', 'presharedkey']
const numberKeys = ['jc', 'jmin', 'jmax', 's1', 's2', 's3', 's4', 'h1', 'h2', 'h3', 'h4', 'mtu', 'listenport',
  'persistentkeepalive', 'fwmark', 'table']
const listKeys = ['address', 'allowedips', 'dns']
const maskPattern = /^(?:255|254|252|248|240|224|192|128|0)(?:\.(?:255|254|252|248|240|224|192|128|0)){3}$/
const pemPattern = /^-----(BEGIN|END) [A-Za-z0-9 ]+-----$/

const token = (text, kind) => ({ text, kind })

function comment(line) {
  const found = line.match(/^(\s*)([#;].*)$/)
  return found ? [token(found[1], 'plain'), token(found[2], 'comment')].filter((part) => part.text) : null
}

function words(text) {
  return text.match(/"(?:[^"\\]|\\.)*"|'[^']*'|\S+|\s+/g) || []
}

function argument(word, directive, index) {
  if (/^\s+$/.test(word)) return [token(word, 'plain')]
  if (/^["'].*["']$/.test(word)) {
    const inner = word.slice(1, -1)
    const parts = directive === 'push' ? openvpnLine(inner) : [token(inner, 'string')]
    return [token(word[0], 'punctuation'), ...parts, token(word[word.length - 1], 'punctuation')]
  }
  if (maskDirectives.includes(directive) && index === 2 && maskPattern.test(word)) return [token(word, 'mask')]
  if (/^\d+$/.test(word)) return [token(word, index === 2 && /^remote|^port|^lport|^rport/.test(directive) ? 'port' : 'number')]
  if (/^\d+\.\d+$/.test(word)) return [token(word, 'number')]
  if (keywords.has(word) || /^[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)*(?::[A-Z0-9-]+)*$/.test(word)) return [token(word, 'keyword')]
  if (/^[/~.]/.test(word)) return [token(word, 'string')]
  return scalar(word)
}

function openvpnLine(line) {
  const remark = comment(line)
  if (remark) return remark
  const result = []
  let directive = ''
  let index = 0
  for (const word of words(line)) {
    if (/^\s+$/.test(word)) {
      result.push(token(word, 'plain'))
    } else if (!directive) {
      directive = word
      result.push(token(word, 'directive'))
      index = 1
    } else {
      result.push(...argument(word, directive, index))
      index++
    }
  }
  return result
}

function pemLine(line, secret) {
  if (pemPattern.test(line.trim())) return [token(line, 'pem')]
  if (/^#/.test(line.trim())) return [token(line, 'comment')]
  return [token(line, secret ? 'secret' : 'base64')]
}

export function highlightOpenVPN(text) {
  let block = ''
  let position = 0
  return text.split('\n').map((line) => {
    const tag = line.trim().match(/^<(\/?)([a-z0-9-]+)>$/)
    if (tag) {
      block = tag[1] ? '' : tag[2]
      position = 0
      return [token(line, 'tag')]
    }
    if (!block) return openvpnLine(line)
    position++
    if (block === 'connection') return openvpnLine(line)
    if (block === 'auth-user-pass') return [token(line, position === 1 ? 'string' : 'secret')]
    return pemLine(line, secretBlocks.includes(block))
  })
}

function list(value) {
  return value.split(/(\s*,\s*)/).flatMap((part) => /,/.test(part) ? [token(part, 'punctuation')] : part ? scalar(part) : [])
}

export function highlightAmnezia(text) {
  return text.split('\n').map((line) => {
    const remark = comment(line)
    if (remark) return remark
    if (/^\s*\[[^\]]+\]\s*$/.test(line)) return [token(line, 'tag')]
    const found = line.match(/^(\s*)([^=\s]+)(\s*=\s*)(.*?)(\s*)$/)
    if (!found) return [token(line, 'plain')]
    const [, lead, key, equals, value, trail] = found
    const name = key.toLowerCase()
    let parts = [token(value, 'string')]
    if (secretKeys.includes(name)) parts = [token(value, 'secret')]
    else if (name === 'publickey') parts = [token(value, 'base64')]
    else if (numberKeys.includes(name) && /^\d+$/.test(value)) parts = [token(value, 'number')]
    else if (listKeys.includes(name)) parts = list(value)
    else if (name === 'endpoint') parts = scalar(value)
    return [token(lead, 'plain'), token(key, 'key'), token(equals, 'punctuation'), ...parts, token(trail, 'plain')]
      .filter((part) => part.text)
  })
}

export function highlightPEM(text) {
  let secret = false
  return text.split('\n').map((line) => {
    const found = line.trim().match(pemPattern)
    if (found) {
      secret = found[1] === 'BEGIN' && /PRIVATE KEY|Static key/i.test(line)
      return [token(line, 'pem')]
    }
    return pemLine(line, secret)
  })
}

export const highlighters = { openvpn: highlightOpenVPN, amneziawg: highlightAmnezia, pem: highlightPEM }
