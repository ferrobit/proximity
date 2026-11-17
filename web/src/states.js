export const stateTones = { up: 'good', connecting: 'busy', reconnecting: 'busy', down: 'bad', failed: 'bad', disabled: 'off', stopped: 'off' }
export const toneTokens = { good: 'colorSuccess', busy: 'colorInfo', warn: 'colorWarning', bad: 'colorError', off: 'colorTextQuaternary' }
export const toneBadges = { good: 'success', busy: 'processing', warn: 'warning', bad: 'error', off: 'default' }
export const toneLegend = [['good', 'good'], ['busy', 'connecting'], ['warn', 'warning'], ['bad', 'down'], ['off', 'off']]
export const highlightedTones = ['busy', 'warn', 'bad']
export const toneSeverity = { bad: 3, busy: 2, warn: 1 }

export function stateTone(state) {
  return stateTones[state] || 'off'
}

export function linkTone(record) {
  return record.enabled && record.status ? stateTone(record.status.state) : 'off'
}

export function targetTone(targets, id) {
  const target = targets.find((item) => item.id === id)
  if (!target) return 'bad'
  return target.enabled ? stateTone(target.state) : 'off'
}

export function routeTone(route, targets, warned) {
  if (!route.enabled) return 'off'
  const tones = [targets.length ? targetTone(targets, route.target) : 'good', warned ? 'warn' : 'good']
  return tones.reduce((result, tone) => (toneSeverity[tone] || 0) > (toneSeverity[result] || 0) ? tone : result, 'good')
}

export function rowTone(tone, enabled = true) {
  if (!enabled || tone === 'off') return 'row-tone row-tone-off row-off'
  return highlightedTones.includes(tone) ? `row-tone row-tone-${tone}` : ''
}

export function tonePalette(token) {
  return {
    ...Object.fromEntries(Object.entries(toneTokens).map(([tone, name]) => [`--tone-${tone}`, token[name]])),
    '--tone-surface': token.colorBgContainer
  }
}
