<template>
  <a-card size="small" class="connections-chart">
    <template #title><SwapOutlined /> Connections</template>
    <template #extra><span class="muted">last {{ rangeLabel }}</span></template>
    <div class="chart-summary">
      <span class="summary-item"><span class="muted">now</span> <b>{{ current }}</b></span>
      <span class="summary-item"><span class="muted">peak</span> <b>{{ peak }}</b></span>
      <span class="summary-item"><span class="muted">average</span> <b>{{ average }}</b></span>
    </div>
    <div ref="box" class="chart-box" :style="{ height: `${height}px` }">
      <svg v-if="width" :width="width" :height="height" role="img" :aria-label="`Active connections for the last ${rangeLabel}`">
        <g class="chart-grid">
          <g v-for="tick in yTicks" :key="`y${tick}`">
            <line :x1="margin.left" :x2="width - margin.right" :y1="y(tick)" :y2="y(tick)" />
            <text :x="margin.left - 8" :y="y(tick)" text-anchor="end" dominant-baseline="middle">{{ tick }}</text>
          </g>
          <text v-for="tick in xTicks" :key="`x${tick}`" :x="x(tick)" :y="height - 6" text-anchor="middle">{{ clock(tick) }}</text>
        </g>
        <path :d="area" :fill="color" fill-opacity=".14" stroke="none" />
        <path :d="line" fill="none" :stroke="color" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
        <g v-if="hover">
          <line class="chart-crosshair" :x1="x(hover.time)" :x2="x(hover.time)" :y1="margin.top" :y2="height - margin.bottom" />
          <circle :cx="x(hover.time)" :cy="y(hover.value)" r="4" :fill="color" class="chart-dot" />
        </g>
        <rect :x="margin.left" :y="margin.top" :width="Math.max(plotWidth, 0)" :height="Math.max(plotHeight, 0)" fill="transparent"
          @pointermove="point" @pointerleave="hover = null" />
      </svg>
      <div v-if="!shown.length" class="chart-empty muted">No samples yet</div>
      <div v-if="hover" class="chart-tooltip" :style="tooltipStyle">
        <div class="tooltip-time">{{ clock(hover.time, true) }}</div>
        <div><b>{{ hover.value }}</b> <span class="muted">connections</span></div>
      </div>
    </div>
  </a-card>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { SwapOutlined } from '@ant-design/icons-vue'
import { theme } from 'ant-design-vue'
import { clock, ranges, timeTicks } from './history'

const props = defineProps({
  samples: { type: Array, required: true },
  step: { type: Number, default: 5 },
  range: { type: Number, default: 900 }
})
const { token } = theme.useToken()

const color = '#0e9f97'
const margin = { top: 12, right: 16, bottom: 24, left: 44 }
const height = 260

const hover = ref(null)
const box = ref(null)
const width = ref(0)
let observer = null

const colors = computed(() => ({ text: token.value.colorTextSecondary, muted: token.value.colorTextTertiary,
  grid: token.value.colorBorderSecondary, surface: token.value.colorBgElevated, shadow: token.value.boxShadowSecondary }))
const rangeLabel = computed(() => ranges.find((item) => item.value === props.range).label)
const plotWidth = computed(() => width.value - margin.left - margin.right)
const plotHeight = computed(() => height - margin.top - margin.bottom)

const end = computed(() => {
  const last = props.samples[props.samples.length - 1]
  return last ? last.time : Math.floor(Date.now() / 1000)
})

const shown = computed(() => props.samples.filter((sample) => sample.time >= end.value - props.range))
const counts = computed(() => shown.value.map((sample) => sample.connections || 0))
const current = computed(() => counts.value.length ? counts.value[counts.value.length - 1] : 0)
const peak = computed(() => counts.value.reduce((result, count) => Math.max(result, count), 0))
const average = computed(() => counts.value.length
  ? Math.round(counts.value.reduce((sum, count) => sum + count, 0) / counts.value.length) : 0)

const yMax = computed(() => {
  for (const factor of [4, 8, 12, 20, 40, 60, 80, 100, 200, 400, 600, 800, 1000, 2000, 4000, 8000]) {
    if (peak.value <= factor) return factor
  }
  return Math.ceil(peak.value / 4000) * 4000
})

const yTicks = computed(() => [0, 1, 2, 3, 4].map((index) => (yMax.value * index) / 4))

const xTicks = computed(() => {
  const spacing = props.range <= 300 ? 120 : props.range <= 900 ? 300 : 1200
  return timeTicks(end.value, props.range, spacing)
})

const segments = computed(() => {
  const result = []
  let previous = null
  for (const sample of shown.value) {
    if (previous === null || sample.time - previous > props.step * 2.5) {
      result.push([])
    }
    result[result.length - 1].push(sample)
    previous = sample.time
  }
  return result
})

const line = computed(() => segments.value.map((segment) => segment
  .map((sample, index) => `${index ? 'L' : 'M'}${x(sample.time).toFixed(1)},${y(sample.connections || 0).toFixed(1)}`)
  .join('')).join(''))

const area = computed(() => segments.value.map((segment) => {
  const bottom = y(0).toFixed(1)
  const first = x(segment[0].time).toFixed(1)
  const last = x(segment[segment.length - 1].time).toFixed(1)
  const top = segment.map((sample) => `L${x(sample.time).toFixed(1)},${y(sample.connections || 0).toFixed(1)}`).join('')
  return `M${first},${bottom}${top}L${last},${bottom}Z`
}).join(''))

const tooltipStyle = computed(() => {
  const left = x(hover.value.time)
  return left > width.value / 2 ? { right: `${width.value - left + 12}px`, top: `${margin.top}px` } : { left: `${left + 12}px`, top: `${margin.top}px` }
})

function x(time) {
  return margin.left + ((time - (end.value - props.range)) / props.range) * plotWidth.value
}

function y(amount) {
  return margin.top + plotHeight.value - (amount / yMax.value) * plotHeight.value
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
  hover.value = { time: nearest.time, value: nearest.connections || 0 }
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
.chart-summary { display: flex; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 8px; padding: 2px 0; }
.summary-item { font-variant-numeric: tabular-nums; }
.chart-box { position: relative; width: 100%; }
.chart-grid line { stroke: v-bind('colors.grid'); stroke-width: 1; }
.chart-grid text { fill: v-bind('colors.text'); font-size: 11px; font-variant-numeric: tabular-nums; }
.chart-crosshair { stroke: #8a8984; stroke-width: 1; }
.chart-dot { stroke: v-bind('colors.surface'); stroke-width: 2; }
.chart-empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; pointer-events: none; }
.chart-tooltip { position: absolute; pointer-events: none; background: v-bind('colors.surface'); border-radius: 6px; padding: 6px 10px; box-shadow: v-bind('colors.shadow'); font-size: 12px; white-space: nowrap; z-index: 2; }
.tooltip-time { color: v-bind('colors.muted'); margin-bottom: 2px; }
</style>
