<template>
  <a-card size="small" class="traffic-chart">
    <template #title><LineChartOutlined /> Throughput</template>
    <template #extra>
      <div class="chart-controls">
        <a-radio-group v-model:value="direction" size="small" button-style="solid">
          <a-radio-button value="sum">Down + Up</a-radio-button>
          <a-radio-button value="download">Download</a-radio-button>
          <a-radio-button value="upload">Upload</a-radio-button>
        </a-radio-group>
        <a-radio-group :value="range" size="small" @update:value="$emit('update:range', $event)">
          <a-radio-button v-for="option in ranges" :key="option.value" :value="option.value">{{ option.label }}</a-radio-button>
        </a-radio-group>
      </div>
    </template>
    <div class="chart-legend">
      <button v-for="item in series" :key="item.key" type="button" class="legend-item"
        :class="{ off: hidden.has(item.key) }" :aria-pressed="!hidden.has(item.key)"
        @click="toggle(item.key)" @mouseenter="focused = item.key" @mouseleave="focused = ''">
        <svg width="18" height="8" aria-hidden="true"><line x1="1" y1="4" x2="17" y2="4" :stroke="item.color" stroke-width="2" :stroke-dasharray="item.dash" stroke-linecap="round" /></svg>
        <span class="legend-name">{{ item.name }}</span>
        <span class="legend-value">{{ rate(latest(item)) }}</span>
      </button>
    </div>
    <div ref="box" class="chart-box" :style="{ height: `${height}px` }">
      <svg v-if="width" :width="width" :height="height" role="img" :aria-label="`Throughput for the last ${rangeLabel}`">
        <g class="chart-grid">
          <g v-for="tick in yTicks" :key="`y${tick}`">
            <line :x1="margin.left" :x2="width - margin.right" :y1="y(tick)" :y2="y(tick)" />
            <text :x="margin.left - 8" :y="y(tick)" text-anchor="end" dominant-baseline="middle">{{ rate(tick) }}</text>
          </g>
          <text v-for="tick in xTicks" :key="`x${tick}`" :x="x(tick)" :y="height - 6" text-anchor="middle">{{ clock(tick) }}</text>
        </g>
        <g v-for="item in visible" :key="item.key" :class="{ faded: focused && focused !== item.key }">
          <path :d="path(item)" fill="none" :stroke="item.color" stroke-width="2" :stroke-dasharray="item.dash"
            stroke-linejoin="round" stroke-linecap="round" />
        </g>
        <g v-if="endLabels.length" class="chart-labels">
          <text v-for="label in endLabels" :key="label.key" :x="width - margin.right + 8" :y="label.y" dominant-baseline="middle">{{ label.name }}</text>
        </g>
        <g v-if="hover">
          <line class="chart-crosshair" :x1="x(hover.time)" :x2="x(hover.time)" :y1="margin.top" :y2="height - margin.bottom" />
          <circle v-for="row in hover.rows" :key="row.key" :cx="x(hover.time)" :cy="y(row.value)" r="4" :fill="row.color" class="chart-dot" />
        </g>
        <rect :x="margin.left" :y="margin.top" :width="Math.max(plotWidth, 0)" :height="Math.max(plotHeight, 0)" fill="transparent"
          @pointermove="point" @pointerleave="hover = null" />
      </svg>
      <div v-if="!shown.length" class="chart-empty muted">No traffic samples yet: a new point is added every {{ step }} s</div>
      <div v-if="hover" class="chart-tooltip" :style="tooltipStyle">
        <div class="tooltip-time">{{ clock(hover.time, true) }}</div>
        <div v-for="row in hover.rows" :key="row.key" class="tooltip-row">
          <svg width="14" height="8" aria-hidden="true"><line x1="1" y1="4" x2="13" y2="4" :stroke="row.color" stroke-width="2" :stroke-dasharray="row.dash" stroke-linecap="round" /></svg>
          <b>{{ rate(row.value) }}</b>
          <span class="muted">{{ row.name }}</span>
        </div>
      </div>
    </div>
  </a-card>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { LineChartOutlined } from '@ant-design/icons-vue'
import { theme } from 'ant-design-vue'
import { rate } from '../format'
import { clock, ranges, timeTicks } from './history'

const props = defineProps({
  overview: { type: Object, required: true },
  samples: { type: Array, required: true },
  step: { type: Number, default: 5 },
  range: { type: Number, default: 900 }
})
defineEmits(['update:range'])
const { token } = theme.useToken()

const palette = ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#6250d6', '#e34948']
const otherColor = '#8a8984'
const margin = { top: 12, right: 112, bottom: 24, left: 72 }
const height = 260

const direction = ref('sum')
const hidden = ref(new Set())
const focused = ref('')
const hover = ref(null)
const box = ref(null)
const width = ref(0)
let observer = null

const colors = computed(() => ({ text: token.value.colorTextSecondary, muted: token.value.colorTextTertiary,
  grid: token.value.colorBorderSecondary, hover: token.value.colorFillTertiary, surface: token.value.colorBgElevated,
  shadow: token.value.boxShadowSecondary }))
const rangeLabel = computed(() => ranges.find((item) => item.value === props.range).label)
const plotWidth = computed(() => width.value - margin.left - margin.right)
const plotHeight = computed(() => height - margin.top - margin.bottom)

const series = computed(() => {
  const collected = props.overview.outbounds.filter((item) => item.enabled && item.statistics).map((item) => item.id)
  const named = collected.length > palette.length ? collected.slice(0, palette.length - 1) : collected
  const rest = collected.slice(named.length)
  const result = named.map((id, index) => ({ key: `e:${id}`, name: id, ids: [id], color: palette[index], dash: '' }))
  if (rest.length) {
    result.push({ key: 'other', name: `Other (${rest.length})`, ids: rest, color: otherColor, dash: '' })
  }
  result.push({ key: 'total', name: 'Total', ids: null, color: colors.value.text, dash: '6 4' })
  return result
})

const visible = computed(() => series.value.filter((item) => !hidden.value.has(item.key)))

const end = computed(() => {
  const last = props.samples[props.samples.length - 1]
  return last ? last.time : Math.floor(Date.now() / 1000)
})

const shown = computed(() => props.samples.filter((sample) => sample.time >= end.value - props.range))

const yMax = computed(() => {
  let result = 0
  for (const sample of shown.value) {
    for (const item of visible.value) {
      result = Math.max(result, value(item, sample))
    }
  }
  return niceCeiling(result)
})

const yTicks = computed(() => [0, 1, 2, 3, 4].map((index) => (yMax.value * index) / 4))

const xTicks = computed(() => timeTicks(end.value, props.range, props.range <= 300 ? 60 : props.range <= 900 ? 180 : 600))

const endLabels = computed(() => {
  if (visible.value.length > 4 || !shown.value.length) return []
  const last = shown.value[shown.value.length - 1]
  const result = visible.value.map((item) => ({ key: item.key, name: item.name, y: y(value(item, last)) }))
  result.sort((first, second) => first.y - second.y)
  for (let index = 1; index < result.length; index++) {
    result[index].y = Math.max(result[index].y, result[index - 1].y + 14)
  }
  for (let index = result.length - 1; index >= 0; index--) {
    result[index].y = Math.min(result[index].y, index === result.length - 1 ? height - margin.bottom : result[index + 1].y - 14)
  }
  return result
})

const tooltipStyle = computed(() => {
  const left = x(hover.value.time)
  return left > width.value / 2 ? { right: `${width.value - left + 12}px`, top: `${margin.top}px` } : { left: `${left + 12}px`, top: `${margin.top}px` }
})

function niceCeiling(maximum) {
  if (maximum <= 0) return 1024
  const unit = Math.pow(1024, Math.max(0, Math.floor(Math.log(maximum) / Math.log(1024))))
  const scaled = maximum / unit
  for (const factor of [1, 2, 4, 8, 12, 20, 40, 60, 80, 100, 200, 400, 600, 800, 1000, 1024]) {
    if (scaled <= factor) return factor * unit
  }
  return 1024 * unit
}

function pick(pair) {
  if (!pair) return 0
  if (direction.value === 'upload') return pair[0]
  if (direction.value === 'download') return pair[1]
  return pair[0] + pair[1]
}

function value(item, sample) {
  if (!item.ids) return pick(sample.total)
  return item.ids.reduce((sum, id) => sum + pick(sample.outbounds[id]), 0)
}

function latest(item) {
  const last = props.samples[props.samples.length - 1]
  return last ? value(item, last) : 0
}

function x(time) {
  return margin.left + ((time - (end.value - props.range)) / props.range) * plotWidth.value
}

function y(amount) {
  return margin.top + plotHeight.value - (amount / yMax.value) * plotHeight.value
}

function path(item) {
  let result = ''
  let previous = null
  for (const sample of shown.value) {
    const command = previous === null || sample.time - previous > props.step * 2.5 ? 'M' : 'L'
    result += `${command}${x(sample.time).toFixed(1)},${y(value(item, sample)).toFixed(1)}`
    previous = sample.time
  }
  return result
}

function toggle(key) {
  const result = new Set(hidden.value)
  if (!result.delete(key)) {
    result.add(key)
  }
  hidden.value = result
}

function point(event) {
  if (!shown.value.length) return
  const bounds = event.currentTarget.ownerSVGElement.getBoundingClientRect()
  const time = end.value - props.range + ((event.clientX - bounds.left - margin.left) / plotWidth.value) * props.range
  let nearest = shown.value[0]
  for (const sample of shown.value) {
    if (Math.abs(sample.time - time) < Math.abs(nearest.time - time)) {
      nearest = sample
    }
  }
  const rows = visible.value.map((item) => ({ key: item.key, name: item.name, color: item.color, dash: item.dash, value: value(item, nearest) }))
  rows.sort((first, second) => second.value - first.value)
  hover.value = { time: nearest.time, rows }
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
.chart-controls { display: flex; gap: 8px; flex-wrap: wrap; }
.chart-legend { display: flex; flex-wrap: wrap; gap: 4px 12px; margin-bottom: 8px; }
.legend-item { display: inline-flex; align-items: center; gap: 6px; padding: 2px 6px; border: 0; border-radius: 4px; background: none; cursor: pointer; font: inherit; color: inherit; }
.legend-item:hover, .legend-item:focus-visible { background: v-bind('colors.hover'); }
.legend-item.off { opacity: .4; }
.legend-item.off .legend-name { text-decoration: line-through; }
.legend-value { color: v-bind('colors.muted'); font-variant-numeric: tabular-nums; }
.chart-box { position: relative; width: 100%; }
.chart-grid line { stroke: v-bind('colors.grid'); stroke-width: 1; }
.chart-grid text, .chart-labels text { fill: v-bind('colors.text'); font-size: 11px; font-variant-numeric: tabular-nums; }
.chart-labels text { font-size: 12px; }
.faded { opacity: .2; }
.chart-crosshair { stroke: #8a8984; stroke-width: 1; }
.chart-dot { stroke: v-bind('colors.surface'); stroke-width: 2; }
.chart-empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; pointer-events: none; }
.chart-tooltip { position: absolute; pointer-events: none; background: v-bind('colors.surface'); border-radius: 6px; padding: 6px 10px; box-shadow: v-bind('colors.shadow'); font-size: 12px; white-space: nowrap; z-index: 2; }
.tooltip-time { color: v-bind('colors.muted'); margin-bottom: 2px; }
.tooltip-row { display: flex; align-items: center; gap: 6px; font-variant-numeric: tabular-nums; }
</style>
