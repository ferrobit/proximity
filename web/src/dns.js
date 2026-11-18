const address = String.raw`(\d{1,3}\.){3}\d{1,3}|\[[0-9a-fA-F:.]+\]`
const host = String.raw`[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?`
const port = String.raw`(:\d{1,5})?`
const patterns = [
  new RegExp(`^(${address})${port}$`),
  /^[0-9a-fA-F]*:[0-9a-fA-F:.]+$/,
  new RegExp(`^(udp|tcp)://(${address})${port}$`),
  new RegExp(`^(tls|quic)://(${address}|${host})${port}$`),
  new RegExp(`^https://(${address}|${host})${port}(/[^\\s#?]*)?$`)
]

export const serversHint = 'One per line: 8.8.8.8, tcp://8.8.8.8, tls://1.1.1.1, https://dns.google/dns-query, quic://dns.adguard-dns.com'
export const carrierHints = {
  udp: 'Carries DNS over UDP and TCP.',
  tcp: 'Carries no UDP: a server given by address is asked over TCP.',
  http: 'An HTTP proxy carries no UDP: a server given by address is asked over TCP. Many HTTP proxies open only port 443: use DNS over HTTPS then.'
}

export function splitServers(text) {
  return String(text || '').split(/[\s,;]+/).filter(Boolean)
}

export async function checkServers(_, text) {
  const servers = splitServers(text)
  const wrong = servers.find((server) => !patterns.some((pattern) => pattern.test(server)))
  if (wrong) throw new Error(`'${wrong}': IP address or udp://, tcp://, tls://, https://, quic:// URL expected`)
  if (servers.length > 8) throw new Error('Up to 8 servers')
}

export function exitOptions(targets, own) {
  const options = own === undefined ? [] : [{ value: '', label: 'This outbound' }]
  options.push({ value: '@host', label: 'Host (directly)' }, { value: '@router', label: 'Router (the routes decide)' })
  for (const [kind, label] of [['outbound', 'Outbounds'], ['fallback', 'Fallbacks']]) {
    const items = targets.filter((item) => item.kind === kind && item.id !== own)
    if (items.length) options.push({ label, options: items.map((item) => ({ value: item.id, label: item.enabled ? item.id : `${item.id} (disabled)` })) })
  }
  return options
}

export function carrierOf(kind, form) {
  if (kind === 'http') return 'http'
  return kind === 'socks5' && !form.socks5.udp ? 'tcp' : 'udp'
}

// "8.8.8.8#Exit" as the router gets it: the exit is shown only when it is not the outbound itself.
export function describeResolvers(resolvers, own) {
  return (resolvers || []).map((item) => {
    const [server, exit] = item.split('#')
    return exit && exit.replace(/~(https|ftp)$/, '') !== own ? `${server} via ${exit === 'RULES' ? 'routes' : exit}` : server
  }).join(', ')
}
