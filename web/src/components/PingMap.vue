<template>
  <div ref="box" class="ping-map">
    <svg v-if="width" :width="width" :height="height" role="img" :aria-label="`Ping of ${result.address} through ${rows.length} outbounds`">
      <defs>
        <marker id="ping-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L8,4 L0,8 z" :fill="colors.edge" />
        </marker>
      </defs>
      <g v-for="row in rows" :key="`in${row.id}`">
        <path :d="curve(left.x + left.width, left.y, middle.x, row.y)" fill="none" :stroke="colors.edge" stroke-width="1.5" />
        <path :d="curve(middle.x + middle.width, row.y, right.x, right.y)" fill="none" :stroke="row.color" :stroke-width="row.system ? 3 : 2"
          :stroke-dasharray="row.replies.length ? '' : '5 4'" :class="{ faded: focused && focused !== row.id }" />
        <g :transform="`translate(${(middle.x + middle.width + right.x) / 2}, ${(row.y + right.y) / 2})`" :class="{ faded: focused && focused !== row.id }">
          <rect :x="-row.label.length * 3.4 - 8" y="-10" :width="row.label.length * 6.8 + 16" height="20" rx="10" :fill="colors.surface" :stroke="row.color" />
          <text text-anchor="middle" dominant-baseline="central" class="edge-label" :fill="colors.text">{{ row.label }}</text>
        </g>
      </g>
      <g class="node">
        <rect :x="left.x" :y="left.y - 30" :width="left.width" height="60" rx="10" :fill="colors.node" :stroke="colors.border" />
        <text :x="left.x + left.width / 2" :y="left.y - 6" text-anchor="middle" class="node-title" :fill="colors.text">Proximity</text>
        <text :x="left.x + left.width / 2" :y="left.y + 14" text-anchor="middle" class="node-note" :fill="colors.muted">{{ rows.length }} ways out</text>
      </g>
      <g v-for="row in rows" :key="row.id" class="node" @pointerenter="focused = row.id" @pointerleave="focused = ''">
        <rect :x="middle.x" :y="row.y - 22" :width="middle.width" height="44" rx="8" :fill="colors.node" :stroke="row.system ? row.color : colors.border"
          :stroke-width="row.system ? 2 : 1" />
        <circle :cx="middle.x + 14" :cy="row.y - 6" r="4" :fill="row.state" />
        <text :x="middle.x + 24" :y="row.y - 6" dominant-baseline="central" class="node-title" :fill="colors.text">{{ row.name }}</text>
        <text :x="middle.x + 14" :y="row.y + 11" dominant-baseline="central" class="node-note" :fill="colors.muted">{{ row.note }}</text>
        <g :transform="`translate(${middle.x + middle.width - 86}, ${row.y + 4})`">
          <line x1="0" x2="76" y1="10" y2="10" :stroke="colors.border" />
          <g v-for="dot in row.dots" :key="dot.index">
            <circle v-if="dot.time !== null" :cx="dot.x" :cy="10 - dot.height" r="2.5" :fill="row.color" />
            <path v-else :d="`M${dot.x - 2.5},${7.5} l5,5 M${dot.x + 2.5},${7.5} l-5,5`" :stroke="colors.bad" stroke-width="1.5" />
          </g>
        </g>
        <text v-if="row.system" :x="middle.x + middle.width - 8" :y="row.y - 6" text-anchor="end" dominant-baseline="central" class="node-badge"
          :fill="row.color">clients ↗</text>
      </g>
      <g class="node">
        <rect :x="right.x" :y="right.y - 30" :width="right.width" height="60" rx="10" :fill="colors.node" :stroke="colors.border" />
        <text :x="right.x + right.width / 2" :y="right.y - 6" text-anchor="middle" class="node-title" :fill="colors.text">{{ result.address }}</text>
        <text :x="right.x + right.width / 2" :y="right.y + 14" text-anchor="middle" class="node-note" :fill="colors.muted">{{ targetNote }}</text>
      </g>
    </svg>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { theme } from 'ant-design-vue'
import { stateTone, toneTokens } from '../states'

const props = defineProps({
  result: { type: Object, required: true }
})
const { token } = theme.useToken()

const box = ref(null)
const width = ref(0)
const focused = ref('')
const rowHeight = 60
let observer = null

const colors = computed(() => ({ text: token.value.colorText, muted: token.value.colorTextTertiary, border: token.value.colorBorder,
  edge: token.value.colorBorder, node: token.value.colorBgContainer, surface: token.value.colorBgElevated,
  good: token.value.colorSuccess, warn: token.value.colorWarning, bad: token.value.colorError }))
const height = computed(() => Math.max(props.result.outbounds.length * rowHeight + 24, 120))
const left = computed(() => ({ x: 4, y: height.value / 2, width: 120 }))
const right = computed(() => ({ x: width.value - 168, y: height.value / 2, width: 164 }))
const middle = computed(() => ({ x: Math.round(width.value * 0.3), width: Math.min(260, Math.round(width.value * 0.3)) }))
const targetNote = computed(() => props.result.host !== props.result.address ? props.result.host : `TCP port ${props.result.port} for proxies`)

const rows = computed(() => props.result.outbounds.map((item, index) => {
  const times = item.replies.map((reply) => reply.time)
  const loss = Math.round(((item.sent - times.length) / item.sent) * 100)
  const average = times.length ? times.reduce((sum, value) => sum + value, 0) / times.length : 0
  const color = !times.length ? colors.value.bad : loss > 0 ? colors.value.warn : colors.value.good
  const label = !times.length ? (item.error ? shorten(item.error) : 'no answer') : `${format(average)} · ${loss}% loss`
  const peak = Math.max(...times, 1)
  const bySequence = new Map(item.replies.map((reply) => [reply.sequence, reply.time]))
  const dots = Array.from({ length: item.sent }, (_, position) => {
    const time = bySequence.has(position + 1) ? bySequence.get(position + 1) : null
    return { index: position, time, x: item.sent > 1 ? (position / (item.sent - 1)) * 72 + 2 : 38, height: time === null ? 0 : 2 + (time / peak) * 10 }
  })
  const state = token.value[toneTokens[stateTone(item.state)]] || colors.value.muted
  const note = `${item.method === 'tcp' ? `TCP :${props.result.port}` : 'ICMP'}${item.path.device ? ` · ${item.path.device}` : ''}${item.path.mark ? ` · mark ${item.path.mark}` : ''}`
  return { id: item.id, name: item.id, y: 12 + rowHeight / 2 + index * rowHeight, replies: item.replies, color, label, dots, state, note,
    system: props.result.capture && props.result.system === item.id }
}))

function format(value) {
  if (value >= 100) return `${Math.round(value)} ms`
  return value >= 1 ? `${value.toFixed(1)} ms` : `${value.toFixed(2)} ms`
}

function shorten(text) {
  return text.length > 28 ? `${text.slice(0, 27)}…` : text
}

function curve(x1, y1, x2, y2) {
  const middleX = (x1 + x2) / 2
  return `M${x1},${y1} C${middleX},${y1} ${middleX},${y2} ${x2},${y2}`
}

onMounted(() => {
  observer = new ResizeObserver((entries) => {
    width.value = Math.max(Math.floor(entries[0].contentRect.width), 640)
  })
  observer.observe(box.value)
})

onUnmounted(() => {
  if (observer) observer.disconnect()
})
</script>

<style scoped>
.ping-map { width: 100%; overflow-x: auto; }
.node-title { font-size: 13px; font-weight: 600; }
.node-note { font-size: 11px; }
.node-badge { font-size: 11px; font-weight: 600; }
.edge-label { font-size: 11px; font-variant-numeric: tabular-nums; }
.faded { opacity: .2; }
</style>
