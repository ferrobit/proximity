<template>
  <a-card size="small" class="hits-chart">
    <template #title><BarChartOutlined /> Route hits</template>
    <template #extra>
      <div class="chart-controls">
        <span class="muted">per {{ bucketLabel }}</span>
        <a-radio-group v-model:value="range" size="small">
          <a-radio-button v-for="option in ranges" :key="option.value" :value="option.value">{{ option.label }}</a-radio-button>
        </a-radio-group>
        <a-popconfirm v-if="canClear" title="Reset all hit counters?" @confirm="reset">
          <a-button size="small"><template #icon><ClearOutlined /></template>Reset</a-button>
        </a-popconfirm>
      </div>
    </template>
    <div v-if="series.length" class="chart-legend">
      <button v-for="item in series" :key="item.key" type="button" class="legend-item"
        :class="{ off: hidden.has(item.key) }" :aria-pressed="!hidden.has(item.key)"
        @click="toggle(item.key)" @mouseenter="focused = item.key" @mouseleave="focused = ''">
        <span class="legend-swatch" :style="{ background: item.color }" />
        <span class="legend-name">{{ item.name }}</span>
        <span class="legend-value">{{ count(item.total) }}</span>
      </button>
    </div>
    <div ref="box" class="chart-box" :style="{ height: `${height}px` }">
      <svg v-if="width" :width="width" :height="height" role="img" :aria-label="`Route hits for the last ${rangeLabel}`">
        <g class="chart-grid">
          <g v-for="tick in yTicks" :key="`y${tick}`">
            <line :x1="margin.left" :x2="width - margin.right" :y1="y(tick)" :y2="y(tick)" />
            <text :x="margin.left - 8" :y="y(tick)" text-anchor="end" dominant-baseline="middle">{{ count(tick) }}</text>
          </g>
          <text v-for="tick in xTicks" :key="`x${tick}`" :x="edge(tick)" :y="height - 6" text-anchor="middle">{{ clock(tick) }}</text>
        </g>
        <g v-for="bar in bars" :key="bar.time" :class="{ dimmed: hover && hover.time !== bar.time }">
          <path v-for="part in bar.parts" :key="part.key" :d="part.path" :fill="part.color"
            :class="{ faded: focused && focused !== part.key }" />
        </g>
        <rect v-for="bar in bars" :key="`hit${bar.time}`" :x="center(bar.time) - slot / 2" :y="margin.top" :width="slot"
          :height="plotHeight" fill="transparent" @pointerenter="hover = bar" @pointerleave="hover = null" />
      </svg>
      <div v-if="!total" class="chart-empty muted">No hits in the last {{ rangeLabel }}: a new point is added every {{ step }} s</div>
      <div v-if="hover && hover.rows.length" class="chart-tooltip" :style="tooltipStyle">
        <div class="tooltip-time">{{ clock(hover.time, true) }} – {{ clock(hover.time + bucket, true) }}</div>
        <div v-for="row in hover.rows" :key="row.key" class="tooltip-row">
          <span class="legend-swatch" :style="{ background: row.color }" />
          <b>{{ count(row.value) }}</b>
          <span class="muted">{{ row.name }}</span>
        </div>
      </div>
    </div>
  </a-card>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { BarChartOutlined, ClearOutlined } from '@ant-design/icons-vue'
import { theme } from 'ant-design-vue'
import { clock, ranges, timeTicks } from './history'

const props = defineProps({
  samples: { type: Array, required: true },
  step: { type: Number, default: 5 },
  origins: { type: Array, required: true },
  canClear: { type: Boolean, default: false }
})
const emit = defineEmits(['clear'])
const { token } = theme.useToken()

const palette = ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#6250d6', '#e34948']
const otherColor = '#8a8984'
const margin = { top: 12, right: 16, bottom: 24, left: 56 }
const height = 220
const columns = 30

const range = ref(900)
const cleared = ref(0)
const hidden = ref(new Set())
const focused = ref('')
const hover = ref(null)
const box = ref(null)
const width = ref(0)
let observer = null

const colors = computed(() => ({ text: token.value.colorTextSecondary, muted: token.value.colorTextTertiary,
  grid: token.value.colorBorderSecondary, hover: token.value.colorFillTertiary, surface: token.value.colorBgElevated,
  shadow: token.value.boxShadowSecondary }))
const rangeLabel = computed(() => ranges.find((item) => item.value === range.value).label)
const bucket = computed(() => Math.max(props.step, range.value / columns))
const bucketLabel = computed(() => bucket.value >= 60 ? `${bucket.value / 60} min` : `${bucket.value} s`)
const plotWidth = computed(() => width.value - margin.left - margin.right)
const plotHeight = computed(() => height - margin.top - margin.bottom)
const slot = computed(() => plotWidth.value / columns)

const end = computed(() => {
  const last = props.samples[props.samples.length - 1]
  const time = last ? last.time : Math.floor(Date.now() / 1000)
  return Math.ceil(time / bucket.value) * bucket.value
})
const start = computed(() => end.value - range.value)
const shown = computed(() => props.samples.filter((sample) => sample.time > Math.max(start.value, cleared.value) &&
  sample.time <= end.value))

const totals = computed(() => {
  const result = {}
  for (const sample of shown.value) {
    for (const [key, value] of Object.entries(sample.hits || {})) {
      result[key] = (result[key] || 0) + value
    }
  }
  return result
})
const total = computed(() => Object.values(totals.value).reduce((sum, value) => sum + value, 0))

const series = computed(() => {
  const order = props.origins.map((item) => item.key)
  const known = new Map(props.origins.map((item) => [item.key, item.name]))
  const keys = Object.keys(totals.value).filter((key) => totals.value[key] > 0)
  keys.sort((first, second) => totals.value[second] - totals.value[first])
  const named = keys.length > palette.length ? keys.slice(0, palette.length - 1) : keys
  const rest = keys.slice(named.length)
  named.sort((first, second) => rank(order, first) - rank(order, second))
  const result = named.map((key, index) => ({ key, name: known.get(key) || label(key), keys: [key], color: palette[index],
    total: totals.value[key] }))
  if (rest.length) {
    result.push({ key: 'other', name: `Other (${rest.length})`, keys: rest, color: otherColor,
      total: rest.reduce((sum, key) => sum + totals.value[key], 0) })
  }
  return result
})
const visible = computed(() => series.value.filter((item) => !hidden.value.has(item.key)))

const buckets = computed(() => {
  const result = []
  for (let index = 0; index < columns; index++) {
    result.push({ time: start.value + index * bucket.value, hits: {} })
  }
  for (const sample of shown.value) {
    const target = result[Math.min(columns - 1, Math.floor((sample.time - 1 - start.value) / bucket.value))]
    for (const [key, value] of Object.entries(sample.hits || {})) {
      target.hits[key] = (target.hits[key] || 0) + value
    }
  }
  return result
})

const yMax = computed(() => {
  let result = 0
  for (const item of buckets.value) {
    result = Math.max(result, visible.value.reduce((sum, entry) => sum + amount(entry, item), 0))
  }
  return niceCeiling(result)
})
const yTicks = computed(() => [0, 1, 2, 3, 4].map((index) => (yMax.value * index) / 4))
const xTicks = computed(() => timeTicks(end.value, range.value, range.value <= 300 ? 60 : range.value <= 900 ? 180 : 600)
  .filter((tick) => tick > start.value && tick < end.value))

const bars = computed(() => buckets.value.map((item) => {
  const thickness = Math.min(24, Math.max(slot.value - 4, 2))
  const left = center(item.time) - thickness / 2
  const parts = []
  let base = 0
  const stacked = visible.value.map((entry) => ({ entry, value: amount(entry, item) })).filter((part) => part.value > 0)
  stacked.forEach((part, index) => {
    const bottom = y(base) - (index ? 1 : 0)
    base += part.value
    const top = y(base) + (index < stacked.length - 1 ? 1 : 0)
    parts.push({ key: part.entry.key, color: part.entry.color, value: part.value, name: part.entry.name,
      path: segment(left, thickness, top, bottom, index === stacked.length - 1) })
  })
  const rows = parts.map((part) => ({ key: part.key, name: part.name, color: part.color, value: part.value }))
  rows.sort((first, second) => second.value - first.value)
  return { time: item.time, parts, rows }
}))

const tooltipStyle = computed(() => {
  const left = center(hover.value.time)
  return left > width.value / 2 ? { right: `${width.value - left + 16}px`, top: `${margin.top}px` }
    : { left: `${left + 16}px`, top: `${margin.top}px` }
})

// NOTE: The page keeps the samples it has loaded: after a reset the older ones are hidden here.
function reset() {
  const last = props.samples[props.samples.length - 1]
  cleared.value = last ? last.time : Math.floor(Date.now() / 1000)
  emit('clear')
}

function label(key) {
  return key.startsWith('inbound:') ? `Inbound ${key.slice('inbound:'.length)}` : key
}

function rank(order, key) {
  const index = order.indexOf(key)
  return index < 0 ? order.length : index
}

function amount(entry, item) {
  return entry.keys.reduce((sum, key) => sum + (item.hits[key] || 0), 0)
}

function niceCeiling(maximum) {
  if (maximum <= 0) return 4
  const unit = Math.pow(10, Math.floor(Math.log10(maximum)))
  for (const factor of [1, 2, 4, 5, 8, 10]) {
    if (maximum <= factor * unit && Number.isInteger((factor * unit) / 4)) return factor * unit
  }
  return 20 * unit
}

function count(value) {
  return Math.round(value).toLocaleString('en-US')
}

function edge(time) {
  return margin.left + ((time - start.value) / bucket.value) * slot.value
}

function center(time) {
  return edge(time) + slot.value / 2
}

function y(value) {
  return margin.top + plotHeight.value - (value / yMax.value) * plotHeight.value
}

function segment(left, thickness, top, bottom, rounded) {
  const right = left + thickness
  const radius = rounded ? Math.min(4, thickness / 2, Math.max(bottom - top, 0)) : 0
  if (bottom - top <= 0.5) return ''
  return `M${left},${bottom}L${left},${top + radius}Q${left},${top} ${left + radius},${top}` +
    `L${right - radius},${top}Q${right},${top} ${right},${top + radius}L${right},${bottom}Z`
}

function toggle(key) {
  const result = new Set(hidden.value)
  if (!result.delete(key)) {
    result.add(key)
  }
  hidden.value = result
}

onMounted(() => {
  observer = new ResizeObserver((entries) => {
    width.value = Math.floor(entries[0].contentRect.width)
  })
  observer.observe(box.value)
})

onUnmounted(() => {
  if (observer) observer.disconnect()
})
</script>

<style scoped>
.hits-chart { margin-bottom: 12px; }
.chart-controls { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.chart-legend { display: flex; flex-wrap: wrap; gap: 4px 12px; margin-bottom: 8px; }
.legend-item { display: inline-flex; align-items: center; gap: 6px; padding: 2px 6px; border: 0; border-radius: 4px; background: none; cursor: pointer; font: inherit; color: inherit; }
.legend-item:hover, .legend-item:focus-visible { background: v-bind('colors.hover'); }
.legend-item.off { opacity: .4; }
.legend-item.off .legend-name { text-decoration: line-through; }
.legend-swatch { display: inline-block; width: 10px; height: 10px; border-radius: 2px; }
.legend-value { color: v-bind('colors.muted'); font-variant-numeric: tabular-nums; }
.chart-box { position: relative; width: 100%; }
.chart-grid line { stroke: v-bind('colors.grid'); stroke-width: 1; }
.chart-grid text { fill: v-bind('colors.text'); font-size: 11px; font-variant-numeric: tabular-nums; }
.faded { opacity: .2; }
.dimmed { opacity: .55; }
.chart-empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; pointer-events: none; }
.chart-tooltip { position: absolute; pointer-events: none; background: v-bind('colors.surface'); border-radius: 6px; padding: 6px 10px; box-shadow: v-bind('colors.shadow'); font-size: 12px; white-space: nowrap; z-index: 2; }
.tooltip-time { color: v-bind('colors.muted'); margin-bottom: 2px; }
.tooltip-row { display: flex; align-items: center; gap: 6px; font-variant-numeric: tabular-nums; }
</style>
