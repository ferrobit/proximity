const keywordDirectives = ['DOMAIN-KEYWORD', 'GEOSITE', 'GEOIP', 'NETWORK', 'PROCESS-NAME', 'PROCESS-PATH']
const numberDirectives = ['DST-PORT', 'SRC-PORT', 'IP-ASN']
const addressDirectives = ['IP-CIDR', 'SRC-IP-CIDR']
const ipv4Pattern = /^(\d{1,3}(?:\.\d{1,3}){3})(\/\d{1,3})?(:\d{1,5})?$/
const ipv6Pattern = /^([0-9a-fA-F]{0,4}(?::[0-9a-fA-F]{0,4}){2,7}(?:\.\d{1,3}){0,3})(\/\d{1,3})?$/
const bracketPattern = /^(\[)([^\]]+)(\])(\/\d{1,3})?(:\d{1,5})?$/
const schemePattern = /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//
const hostPattern = /^[\p{L}\p{N}*?._+-]+$/u
const numberPattern = /^\d+(?:-\d+)?$/

const token = (text, kind) => ({ text, kind })

function prefix(text, kind, limit) {
  const value = Number(text.slice(1))
  return token(text, value > limit ? 'error' : kind)
}

function ipv4(found) {
  const valid = found[1].split('.').every((octet) => Number(octet) <= 255)
  const result = [token(found[1], valid ? 'ip' : 'error')]
  if (found[2]) result.push(prefix(found[2], 'mask', 32))
  if (found[3]) result.push(prefix(found[3], 'port', 65535))
  return result
}

function ipv6(address, mask) {
  const result = [token(address, 'ip')]
  if (mask) result.push(prefix(mask, 'mask', 128))
  return result
}

function wildcards(text, kind) {
  return text.split(/([*?]+)/).filter(Boolean).map((part) => token(part, /^[*?]+$/.test(part) ? 'wildcard' : kind))
}

function host(text) {
  const result = []
  let rest = text
  const lead = rest.match(/^(\*\.|\+\.|\.)/)
  if (lead) {
    result.push(token(lead[1], 'wildcard'))
    rest = rest.slice(lead[1].length)
  }
  const labels = rest.split('.')
  const base = labels.length > 1 && labels[labels.length - 1] === '' ? 3 : 2
  const split = Math.max(0, labels.length - base)
  if (split > 0) {
    result.push(...wildcards(labels.slice(0, split).join('.') + '.', 'subdomain'))
  }
  result.push(...wildcards(labels.slice(split).join('.'), 'domain'))
  return result
}

function address(value) {
  let found = value.match(ipv4Pattern)
  if (found) return ipv4(found)
  found = value.match(bracketPattern)
  if (found) {
    const result = [token(found[1], 'punctuation'), ...ipv6(found[2]), token(found[3], 'punctuation')]
    if (found[4]) result.push(prefix(found[4], 'mask', 128))
    if (found[5]) result.push(prefix(found[5], 'port', 65535))
    return result
  }
  found = value.match(ipv6Pattern)
  if (found && value.includes('::') || found && value.split(':').length > 3) return ipv6(found[1], found[2])
  return null
}

function location(value) {
  const result = []
  let rest = value
  const scheme = rest.match(schemePattern)
  if (scheme) {
    result.push(token(scheme[0], 'muted'))
    rest = rest.slice(scheme[0].length)
  }
  const end = rest.search(scheme ? /[/?#]/ : /\//)
  const tail = end >= 0 ? rest.slice(end) : ''
  let name = end >= 0 ? rest.slice(0, end) : rest
  const ip = address(name)
  if (ip) {
    result.push(...ip)
  } else {
    let port = ''
    const found = name.match(/^(.*?)(:\d{1,5})$/)
    if (found) {
      name = found[1]
      port = found[2]
    }
    if (!name || !hostPattern.test(name)) return null
    result.push(...host(name))
    if (port) result.push(prefix(port, 'port', 65535))
  }
  if (tail) result.push(token(tail, 'muted'))
  return result
}

function value(text, directive) {
  if (!text) return []
  if (keywordDirectives.includes(directive)) return [token(text, 'keyword')]
  if (directive === 'DOMAIN-REGEX') return [token(text, 'regex')]
  if (numberDirectives.includes(directive)) return [token(text, numberPattern.test(text) ? 'number' : 'error')]
  if (addressDirectives.includes(directive)) return address(text) || [token(text, 'error')]
  return address(text) || location(text) || [token(text, numberPattern.test(text) ? 'number' : 'plain')]
}

export function highlightLine(line, directive) {
  if (/^\s*#/.test(line)) return [token(line, 'comment')]
  const result = []
  const lead = line.match(/^\s*/)[0]
  if (lead) result.push(token(lead, 'plain'))
  let body = line.slice(lead.length)
  const trail = body.match(/\s*$/)[0]
  body = body.slice(0, body.length - trail.length)
  const comma = body.indexOf(',')
  const main = comma >= 0 ? body.slice(0, comma) : body
  result.push(...value(main, directive))
  if (comma >= 0) {
    result.push(token(',', 'punctuation'))
    result.push(token(body.slice(comma + 1), 'option'))
  }
  if (trail) result.push(token(trail, 'plain'))
  return result
}

function condition(text) {
  const comma = text.indexOf(',')
  if (comma < 0) return [token(text, 'plain')]
  const directive = text.slice(0, comma)
  let rest = text.slice(comma + 1)
  const result = [token(directive, 'directive'), token(',', 'punctuation')]
  const option = rest.match(/,(no-resolve|src)$/)
  if (option) rest = rest.slice(0, rest.length - option[0].length)
  result.push(...value(rest, directive))
  if (option) result.push(token(',', 'punctuation'), token(option[1], 'option'))
  return result
}

export function highlightRule(line) {
  const logical = line.match(/^(AND|OR|NOT),\(\((.*)\)\),([^,]+)$/)
  if (logical) {
    const result = [token(logical[1], 'logic'), token(',((', 'punctuation')]
    logical[2].split('),(').forEach((part, index) => {
      if (index) result.push(token('),(', 'punctuation'))
      result.push(...condition(part))
    })
    return [...result, token(')),', 'punctuation'), token(logical[3], 'target')]
  }
  let body = line
  let option = ''
  const found = body.match(/,(no-resolve|src)$/)
  if (found) {
    option = found[1]
    body = body.slice(0, body.length - found[0].length)
  }
  const last = body.lastIndexOf(',')
  if (/^MATCH,/.test(body)) return [token('MATCH', 'directive'), token(',', 'punctuation'), token(body.slice(6), 'target')]
  if (body.indexOf(',') === last) return [token(line, 'plain')]
  const result = [...condition(body.slice(0, last)), token(',', 'punctuation'), token(body.slice(last + 1), 'target')]
  if (option) result.push(token(',', 'punctuation'), token(option, 'option'))
  return result
}

function sortKey(line) {
  return line.trim().replace(/^(\*\.|\+\.|\.)/, '').toLowerCase()
}

export function sortLines(text) {
  const lines = text.replace(/\s+$/, '').split('\n')
  const header = []
  while (lines.length && (/^\s*#/.test(lines[0]) || !lines[0].trim())) {
    header.push(lines.shift())
  }
  const groups = []
  let pending = []
  for (const line of lines) {
    if (!line.trim()) continue
    if (/^\s*#/.test(line)) {
      pending.push(line)
    } else {
      groups.push({ key: sortKey(line), lines: [...pending, line] })
      pending = []
    }
  }
  const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })
  groups.sort((left, right) => collator.compare(left.key, right.key))
  return [...header, ...groups.flatMap((group) => group.lines), ...pending].join('\n')
}

export function countLines(text) {
  const lines = text.split('\n').filter((line) => line.trim())
  const comments = lines.filter((line) => /^\s*#/.test(line)).length
  return { entries: lines.length - comments, comments }
}
