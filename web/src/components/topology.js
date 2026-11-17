import { inboundLabels, rate, stateLabels, typeLabels } from '../format'

export const nodeHeight = 62
export const columnTitles = ['Sources', 'Router', 'Routes', 'Fallbacks', 'Outbounds', 'Internet']

const columnWidths = [200, 170, 200, 180, 220, 156]
const gap = 14
const pad = 22
const header = 40
const minimumSpace = 34
const linkStates = { up: 'good', connecting: 'busy', reconnecting: 'busy', down: 'bad', failed: 'bad', disabled: 'off', stopped: 'off' }

const speed = (traffic) => traffic ? (traffic.upload_rate || 0) + (traffic.download_rate || 0) : 0

function rates(traffic) {
  return `↑ ${rate(traffic.upload_rate)}  ↓ ${rate(traffic.download_rate)}`
}

function remote(outbound) {
  if (outbound.type === 'host') {
    const host = outbound.host || {}
    return host.mode === 'interface' ? `${host.interface}${host.gateway ? ` via ${host.gateway}` : ''}` : 'Default routing'
  } else if (outbound.type === 'socks5') {
    return `${outbound.socks5.server}:${outbound.socks5.port}`
  } else if (outbound.type === 'http') {
    return `${outbound.http.server}:${outbound.http.port}`
  } else if (outbound.type === 'openvpn' && outbound.openvpn && outbound.openvpn.config) {
    const found = outbound.openvpn.config.match(/^\s*remote\s+(\S+)(?:\s+(\d+))?/m)
    if (found) return `${found[1]}${found[2] ? `:${found[2]}` : ''}`
  } else if (outbound.type === 'amneziawg' && outbound.amneziawg && outbound.amneziawg.config) {
    const found = outbound.amneziawg.config.match(/^\s*Endpoint\s*=\s*(\S+)/mi)
    if (found) return found[1]
  }
  return outbound.status.gateway ? `gateway ${outbound.status.gateway}` : outbound.status.device || typeLabels[outbound.type]
}

function trafficDetails(record) {
  if (!record.statistics) {
    return [['Statistics', 'off']]
  }
  return [['Rate', rates(record.traffic)], ['Connections', String(record.traffic.connections)]]
}

export function buildGraph(data, overview, showDisabled) {
  const nodes = new Map()
  const edges = []
  const router = overview.router || {}
  const totalRate = speed(overview.total)
  const running = !!router.running
  const add = (node) => {
    nodes.set(node.id, { disabled: false, active: false, state: 'info', details: [], metric: '', ...node })
    return node.id
  }
  const connect = (from, to, extra) => edges.push({ from, to, ...extra })
  const outboundIds = new Set(data.outbounds.map((item) => item.id))
  const fallbackIds = new Set(data.fallbacks.map((item) => item.id))
  const builtin = (name) => {
    const id = `builtin:${name}`
    if (!nodes.has(id)) {
      add(name === 'DIRECT'
        ? { id, column: 4, kind: 'direct', title: 'DIRECT', subtitle: 'Host default routing', details: [['Meaning', 'Connect without a proxy']] }
        : { id, column: 4, kind: 'reject', title: 'REJECT', subtitle: 'Connections are blocked', details: [['Meaning', 'Drop the connection']] })
      if (name === 'DIRECT') connect(id, 'internet')
    }
    return id
  }
  const resolve = (target) => {
    if (target === 'DIRECT' || target === 'REJECT') return builtin(target)
    if (outboundIds.has(target)) return `outbound:${target}`
    if (fallbackIds.has(target)) return `fallback:${target}`
    const id = `missing:${target}`
    if (!nodes.has(id)) {
      add({ id, column: 4, kind: 'missing', title: target, subtitle: 'Target does not exist', state: 'bad' })
    }
    return id
  }
  add({ id: 'internet', column: 5, kind: 'internet', title: 'Internet', subtitle: 'Destinations',
    extra: totalRate > 0 ? `⇅ ${rate(totalRate)}` : '', details: [['Total rate', rates(overview.total || {})]] })
  for (const outbound of data.outbounds) {
    const status = outbound.status
    const details = [['Type', typeLabels[outbound.type]], ['State', stateLabels[status.state] || status.state]]
    if (status.latency >= 0) details.push(['Latency', `${status.latency} ms`])
    if (status.address) details.push(['Address', `${status.device} · ${status.address}`])
    if (outbound.usage === 'unused') details.push(['Usage', 'not used by routes, fallbacks or inbounds'])
    const id = add({ id: `outbound:${outbound.id}`, column: 4, kind: 'outbound', type: outbound.type, record: outbound,
      title: outbound.id, subtitle: remote(outbound), disabled: !outbound.enabled,
      state: outbound.enabled ? linkStates[status.state] || 'off' : 'off', message: status.message,
      active: outbound.statistics && speed(outbound.traffic) > 0, rate: speed(outbound.traffic),
      metric: status.latency >= 0 ? `${status.latency} ms` : stateLabels[status.state] || '',
      extra: outbound.statistics && speed(outbound.traffic) > 0 ? rates(outbound.traffic) : '',
      unused: outbound.usage === 'unused', details: [...details, ...trafficDetails(outbound)] })
    connect(id, 'internet')
  }
  for (const fallback of data.fallbacks) {
    const alive = fallback.outbounds.find((member) => {
      const node = nodes.get(`outbound:${member}`)
      return node && node.state === 'good'
    })
    const id = add({ id: `fallback:${fallback.id}`, column: 3, kind: 'fallback', record: fallback, title: fallback.id,
      subtitle: alive ? `uses ${alive}` : 'No outbound is alive', disabled: !fallback.enabled,
      state: !fallback.enabled ? 'off' : alive ? 'good' : 'bad',
      details: [['Outbounds', fallback.outbounds.join(' → ') || '—'],
        ['Health check', fallback.health_check ? `${fallback.url} every ${fallback.interval}s` : 'off']] })
    fallback.outbounds.forEach((member, index) => connect(id, resolve(member), { label: String(index + 1), preferred: member === alive }))
  }
  add({ id: 'router', column: 1, kind: 'router', title: 'Router', subtitle: `mihomo${router.version ? ` ${router.version}` : ''}`,
    state: running ? overview.failure ? 'warn' : 'good' : router.stopped ? 'off' : 'bad', active: running && totalRate > 0,
    metric: running ? '' : 'stopped', extra: running && totalRate > 0 ? `⇅ ${rate(totalRate)}` : '', message: overview.failure || router.message,
    link: 'settings', details: [['State', running ? 'running' : 'stopped'], ['Rules', String(overview.counts ? overview.counts.rules : 0)],
      ['Connections', String(overview.total ? overview.total.connections : 0)]] })
  if (overview.capture) {
    connect(add({ id: 'entry:host', column: 0, kind: 'entry', icon: 'host', title: 'Host applications', subtitle: 'Transparent capture',
      state: running ? 'good' : 'off', active: running && totalRate > 0, link: 'settings',
      details: [['Meaning', 'All outgoing connections of the host']] }), 'router')
  }
  connect(add({ id: 'entry:proxy', column: 0, kind: 'entry', icon: 'proxy', title: `Proxy port ${overview.proxy_port || ''}`,
    subtitle: 'HTTP and SOCKS5 clients', state: running ? 'good' : 'off', active: running && totalRate > 0, link: 'settings',
    details: [['Meaning', 'Clients that use Proximity as a proxy']] }), 'router')
  for (const inbound of data.inbounds) {
    const status = inbound.status
    const clients = status.clients || []
    const protocol = inbound.type === 'openvpn' ? inbound.openvpn.protocol : inbound.socks5 && inbound.socks5.udp ? 'tcp+udp' : 'tcp'
    const details = [['Type', `${inboundLabels[inbound.type]} inbound`], ['State', stateLabels[status.state] || status.state],
      ['Users', `${inbound.users.filter((user) => user.enabled).length} of ${inbound.users.length}${inbound.anonymous ? ', anonymous allowed' : ''}`]]
    if (inbound.type === 'openvpn') {
      details.push(['Clients', clients.length ? clients.map((client) => `${client.name} ${client.virtual}`).join(', ') : 'none'])
    }
    const id = add({ id: `inbound:${inbound.id}`, column: 0, kind: 'inbound', type: inbound.type, record: inbound, title: inbound.id,
      subtitle: `${inboundLabels[inbound.type]} :${inbound.port}/${protocol}`, disabled: !inbound.enabled,
      state: inbound.enabled ? linkStates[status.state] || 'off' : 'off', message: status.message,
      active: inbound.statistics && speed(inbound.traffic) > 0, rate: speed(inbound.traffic),
      extra: inbound.statistics && speed(inbound.traffic) > 0 ? rates(inbound.traffic) : '',
      metric: inbound.type === 'openvpn' && status.state === 'up' ? `${clients.length} client${clients.length === 1 ? '' : 's'}`
        : stateLabels[status.state] || '', details: [...details, ...trafficDetails(inbound)] })
    connect(id, inbound.outbound === '@router' ? 'router' : inbound.outbound === '@host' ? builtin('DIRECT') : resolve(inbound.outbound),
      { direct: inbound.outbound !== '@router' })
  }
  const warned = new Set(data.warnings.flatMap((item) => item.keys))
  data.routes.forEach((route, index) => {
    const params = route.params.filter((line) => line.trim() && !line.trim().startsWith('#'))
    const id = add({ id: `route:${route.key}`, column: 2, kind: 'route', record: route, title: route.id,
      subtitle: `${route.directive} ${params[0] || ''}${params.length > 1 ? ` +${params.length - 1}` : ''}`,
      disabled: !route.enabled, state: !route.enabled ? 'off' : warned.has(route.key) ? 'warn' : 'info', metric: `#${index + 1}`,
      provided: !!route.provided, details: [['Directive', route.directive], ['Target', route.target],
        ['Parameters', `${params.length} line${params.length === 1 ? '' : 's'}`]] })
    connect('router', id)
    connect(id, resolve(route.target))
  })
  connect('router', add({ id: 'route:@match', column: 2, kind: 'match', title: 'Everything else', subtitle: `MATCH → ${data.defaultTarget}`,
    metric: 'last', details: [['Meaning', 'Connections that no route matched'], ['Target', data.defaultTarget]] }))
  connect('route:@match', resolve(data.defaultTarget))
  for (const node of nodes.values()) {
    if (node.kind === 'fallback') {
      node.active = edges.some((edge) => edge.from === node.id && nodes.get(edge.to).active)
    }
  }
  for (const node of nodes.values()) {
    if (node.kind === 'route' || node.kind === 'match') {
      const edge = edges.find((item) => item.from === node.id)
      node.active = node.state !== 'off' && !!edge && nodes.get(edge.to).active
    }
  }
  return prune(nodes, edges, showDisabled)
}

function prune(nodes, edges, showDisabled) {
  if (!showDisabled) {
    for (const [id, node] of nodes) {
      if (node.disabled) nodes.delete(id)
    }
  }
  let visible = edges.filter((edge) => nodes.has(edge.from) && nodes.has(edge.to))
  for (const [id, node] of nodes) {
    if (['direct', 'reject', 'missing'].includes(node.kind) && !visible.some((edge) => edge.to === id)) {
      nodes.delete(id)
    }
  }
  visible = visible.filter((edge) => nodes.has(edge.from) && nodes.has(edge.to))
  if (!visible.some((edge) => edge.to === 'internet')) {
    nodes.delete('internet')
  }
  return { nodes, edges: visible.filter((edge) => nodes.has(edge.to)) }
}

function bezier(x1, y1, x2, y2, t) {
  const dx = Math.max(40, (x2 - x1) / 2)
  const points = [[x1, y1], [x1 + dx, y1], [x2 - dx, y2], [x2, y2]]
  const weights = [(1 - t) ** 3, 3 * (1 - t) ** 2 * t, 3 * (1 - t) * t * t, t ** 3]
  return points.reduce((sum, point, index) => [sum[0] + point[0] * weights[index], sum[1] + point[1] * weights[index]], [0, 0])
}

function edgeState(from, to) {
  if (from.disabled || to.disabled || from.state === 'off' || to.state === 'off') return 'off'
  const state = to.state === 'info' ? from.state : to.state
  return state === 'info' ? 'good' : state
}

export function layoutGraph(graph, viewportWidth) {
  const all = [...graph.nodes.values()]
  const used = [...new Set(all.map((node) => node.column))].sort((left, right) => left - right)
  const columns = used.map((index) => ({ index, title: columnTitles[index], nodes: all.filter((node) => node.column === index) }))
  const count = columns.length
  const occupied = columns.reduce((sum, column) => sum + columnWidths[column.index], 0)
  const width = Math.max(viewportWidth, pad * 2 + occupied + (count - 1) * minimumSpace)
  const space = count > 1 ? (width - pad * 2 - occupied) / (count - 1) : 0
  let left = pad
  const tallest = Math.max(1, ...columns.map((column) => column.nodes.length))
  const content = tallest * nodeHeight + (tallest - 1) * gap
  const incoming = new Map()
  for (const edge of graph.edges) {
    incoming.set(edge.to, [...(incoming.get(edge.to) || []), edge.from])
  }
  columns.forEach((column) => {
    if (column.index === 3 || column.index === 4) {
      const center = (node) => {
        const sources = (incoming.get(node.id) || []).map((id) => graph.nodes.get(id)).filter((item) => item.y !== undefined)
        return sources.length ? sources.reduce((sum, item) => sum + item.y, 0) / sources.length : Infinity
      }
      const weights = new Map(column.nodes.map((node) => [node.id, center(node)]))
      column.nodes.sort((left, right) => weights.get(left.id) - weights.get(right.id) || 0)
    }
    const height = column.nodes.length * nodeHeight + (column.nodes.length - 1) * gap
    const top = header + pad + (content - height) / 2
    column.x = left
    column.width = columnWidths[column.index]
    left += column.width + space
    column.nodes.forEach((node, index) => {
      node.x = column.x
      node.width = column.width
      node.y = top + index * (nodeHeight + gap)
    })
  })
  const edges = graph.edges.map((edge) => {
    const from = graph.nodes.get(edge.from)
    const to = graph.nodes.get(edge.to)
    const x1 = from.x + from.width
    const y1 = from.y + nodeHeight / 2
    const x2 = to.x
    const y2 = to.y + nodeHeight / 2
    const dx = Math.max(40, (x2 - x1) / 2)
    const state = edgeState(from, to)
    const flowing = state !== 'off' && (to.kind === 'internet' || from.kind === 'inbound' ? from.active : to.active)
    const thickness = to.kind === 'internet' && from.active ? Math.min(7, 2.2 + Math.log10(1 + from.rate / 1024) * 1.4) : flowing ? 2.4 : 1.6
    const [labelX, labelY] = bezier(x1, y1, x2, y2, 0.8)
    return { ...edge, key: `${edge.from}>${edge.to}`, path: `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`,
      state, flowing, thickness, labelX, labelY }
  })
  return { width, height: header + pad * 2 + content, columns, nodes: all, edges }
}

export function related(graph, id) {
  const result = new Set([id])
  const walk = (key, forward) => {
    for (const edge of graph.edges) {
      const [from, to] = forward ? [edge.from, edge.to] : [edge.to, edge.from]
      if (from === key && !result.has(`${forward}:${to}`)) {
        result.add(`${forward}:${to}`)
        result.add(to)
        result.add(edge.key)
        walk(to, forward)
      }
    }
  }
  walk(id, true)
  walk(id, false)
  return result
}
