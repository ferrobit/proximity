const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

export function bytes(value) {
  let amount = Number(value) || 0
  let index = 0
  while (amount >= 1024 && index < units.length - 1) {
    amount /= 1024
    index++
  }
  return `${index === 0 ? amount : amount.toFixed(amount >= 100 ? 0 : 1)} ${units[index]}`
}

export const rate = (value) => `${bytes(value)}/s`

export function ago(seconds) {
  if (!seconds) {
    return '—'
  }
  const elapsed = Math.max(0, Math.floor(Date.now() / 1000) - seconds)
  if (elapsed < 60) return `${elapsed}s ago`
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ago`
  if (elapsed < 86400) return `${Math.floor(elapsed / 3600)}h ago`
  return `${Math.floor(elapsed / 86400)}d ago`
}

export function duration(seconds) {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days) return `${days}d ${hours}h`
  if (hours) return `${hours}h ${minutes}m`
  return `${minutes}m ${seconds % 60}s`
}

export const typeLabels = { host: 'HOST Internet', socks5: 'SOCKS5 Proxy', http: 'HTTP Proxy', openvpn: 'OpenVPN', amneziawg: 'AmneziaWG' }
export const typeColors = { host: 'blue', socks5: 'purple', http: 'geekblue', openvpn: 'orange', amneziawg: 'magenta' }
export const inboundLabels = { socks5: 'SOCKS5', openvpn: 'OpenVPN' }
export const inboundColors = { socks5: 'purple', openvpn: 'orange' }
export const stateColors = { up: 'success', connecting: 'processing', reconnecting: 'warning', down: 'error', failed: 'error', disabled: 'default', stopped: 'default' }
export const stateLabels = { up: 'good', connecting: 'connecting', reconnecting: 'reconnecting', down: 'down', failed: 'failed', disabled: 'disabled', stopped: 'stopped' }
export const usageColors = { enabled: 'green', disabled: 'default', unused: 'gold' }
export const reasonColors = { refused: 'red', timeout: 'orange', dns: 'purple', unreachable: 'volcano', 'no route': 'volcano', reset: 'magenta', error: 'default' }
export const activeStates = ['up', 'connecting', 'reconnecting']

const builtinNames = { DIRECT: 'DIRECT (host default routing)', REJECT: 'REJECT (block)' }
const targetOption = (item) => ({ value: item.id, label: (builtinNames[item.id] || item.id) + (item.enabled ? '' : ' (disabled)') })

export const targetOptions = (targets) => [
  { label: 'Built-in', options: targets.filter((item) => item.kind === 'builtin').map(targetOption) },
  { label: 'Outbounds', options: targets.filter((item) => item.kind === 'outbound').map(targetOption) },
  { label: 'Fallbacks', options: targets.filter((item) => item.kind === 'fallback').map(targetOption) }
]
