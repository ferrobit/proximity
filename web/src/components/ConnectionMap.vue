<template>
  <a-card size="small" :class="['map-card', { 'map-expanded': expanded }]">
    <template #title><ApartmentOutlined /> Connection map</template>
    <template #extra>
      <div class="map-tools">
        <span class="legend"><i class="swatch good" />good</span>
        <span class="legend"><i class="swatch busy" />connecting</span>
        <span class="legend"><i class="swatch warn" />warning</span>
        <span class="legend"><i class="swatch bad" />down</span>
        <span class="legend"><i class="swatch off" />off</span>
        <span class="legend"><svg width="28" height="8" class="legend-flow" :style="palette"><path d="M 1 4 L 27 4" /></svg>traffic</span>
        <a-divider type="vertical" />
        <a-switch v-model:checked="showDisabled" size="small" /><span>Show disabled</span>
        <a-divider type="vertical" />
        <a-button-group size="small">
          <a-tooltip title="Zoom out"><a-button :disabled="zoom <= minimumZoom" @click="zoomBy(1 / 1.25)"><template #icon><ZoomOutOutlined /></template></a-button></a-tooltip>
          <a-tooltip title="Actual size"><a-button class="zoom-value" @click="setZoom(1)">{{ Math.round(zoom * 100) }}%</a-button></a-tooltip>
          <a-tooltip title="Zoom in"><a-button :disabled="zoom >= maximumZoom" @click="zoomBy(1.25)"><template #icon><ZoomInOutlined /></template></a-button></a-tooltip>
          <a-tooltip title="Fit to screen"><a-button :type="fit ? 'primary' : 'default'" @click="fitToScreen"><template #icon><ExpandOutlined /></template></a-button></a-tooltip>
        </a-button-group>
        <a-tooltip title="Refresh"><a-button size="small" @click="load(false)"><template #icon><ReloadOutlined /></template></a-button></a-tooltip>
        <a-tooltip :title="expanded ? 'Exit full screen (Esc)' : 'Full screen'">
          <a-button size="small" @click="expand(!expanded)">
            <template #icon><FullscreenExitOutlined v-if="expanded" /><FullscreenOutlined v-else /></template></a-button>
        </a-tooltip>
      </div>
    </template>
    <a-skeleton v-if="!data" active />
    <div v-else ref="viewport" :class="['map-viewport', { 'map-panning': panning }]" :style="[palette, expanded ? {} : { maxHeight: `${available}px` }]"
      @wheel="wheel" @mousedown="pan">
      <div class="map-sizer" :style="{ width: `${layout.width * zoom}px`, height: `${layout.height * zoom}px` }">
        <div class="map" :style="{ width: `${layout.width}px`, height: `${layout.height}px`, transform: `scale(${zoom})` }">
          <div v-for="column in layout.columns" :key="column.index" class="column-title" :style="{ left: `${column.x}px`, width: `${column.width}px` }">
            {{ column.title }} <span class="column-count">{{ column.nodes.length }}</span>
          </div>
          <svg class="edges" :width="layout.width" :height="layout.height">
            <g v-for="edge in layout.edges" :key="edge.key" :class="['edge', `edge-${edge.state}`, { 'edge-faded': faded(edge.key), 'edge-direct': edge.direct }]">
              <path class="edge-base" :d="edge.path" :stroke-width="edge.thickness" />
              <path v-if="edge.flowing" class="edge-flow" :d="edge.path" :stroke-width="edge.thickness" />
              <g v-if="edge.label" class="edge-label" :class="{ preferred: edge.preferred }">
                <circle :cx="edge.labelX" :cy="edge.labelY" r="8" />
                <text :x="edge.labelX" :y="edge.labelY">{{ edge.label }}</text>
              </g>
            </g>
          </svg>
          <a-popover v-for="node in layout.nodes" :key="node.id" placement="bottom" :mouse-enter-delay="0.4" :overlay-style="{ maxWidth: '360px' }">
            <template #title>
              <div class="popover-title">
                <span><component :is="iconOf(node)" /> {{ node.title }}</span>
                <a-switch v-if="toggleable(node)" size="small" :checked="!node.disabled" :loading="busy === node.id"
                  checked-children="on" un-checked-children="off" @change="toggle(node, $event)" />
              </div>
            </template>
            <template #content>
              <div class="popover-body">
                <div v-for="[label, value] in node.details" :key="label" class="detail"><span class="muted">{{ label }}</span><span>{{ value }}</span></div>
                <a-alert v-if="node.message" type="warning" show-icon :message="node.message" class="detail-alert" />
                <a-space v-if="canEdit() && (controllable(node) || editable(node))" wrap class="detail-actions">
                  <a-button v-if="editable(node)" size="small" type="primary" @click="edit(node)">
                    <template #icon><EyeOutlined v-if="node.provided" /><EditOutlined v-else /></template>{{ node.provided ? 'View' : 'Edit' }}</a-button>
                  <template v-if="controllable(node)">
                    <a-button size="small" :disabled="!startable(node)" :loading="busy === `${node.id}:start`" @click="control(node, 'start')">
                      <template #icon><PlayCircleOutlined /></template>Start</a-button>
                    <a-button size="small" danger :disabled="!stoppable(node)" :loading="busy === `${node.id}:stop`" @click="control(node, 'stop')">
                      <template #icon><PauseCircleOutlined /></template>Stop</a-button>
                    <a-button size="small" :disabled="node.disabled" :loading="busy === `${node.id}:restart`" @click="control(node, 'restart')">
                      <template #icon><SyncOutlined /></template>Restart</a-button>
                  </template>
                </a-space>
                <div v-if="clickHint(node)" class="muted detail-hint">{{ clickHint(node) }}</div>
              </div>
            </template>
            <div :class="['node', `node-${node.state}`, `kind-${node.kind}`, { 'node-disabled': node.disabled, 'node-faded': faded(node.id),
              'node-clickable': clickable(node), 'node-active': node.active }]"
              :style="{ left: `${node.x}px`, top: `${node.y}px`, width: `${node.width}px`, height: `${nodeHeight}px` }"
              @mouseenter="hovered = node.id" @mouseleave="hovered = ''" @click="open(node)">
              <i v-if="hasIncoming.has(node.id)" class="port port-in" />
              <i v-if="hasOutgoing.has(node.id)" class="port port-out" />
              <div class="node-icon" :style="{ background: colorOf(node) }"><component :is="iconOf(node)" /></div>
              <div class="node-text">
                <div class="node-head">
                  <span class="node-title"><LockOutlined v-if="node.provided" class="node-flag" />{{ node.title }}</span>
                    <span class="node-metric">{{ node.metric }}</span>
                  <span class="node-dot" />
                </div>
                <div class="node-subtitle">{{ node.subtitle }}</div>
                <div v-if="node.extra" class="node-extra">{{ node.extra }}</div>
                <div v-else-if="node.unused" class="node-extra"><span class="node-tag">unused</span></div>
              </div>
            </div>
          </a-popover>
        </div>
      </div>
    </div>
    <OutboundDialog v-model:open="dialogs.outbound" :outbound="selected" :kind="selected ? selected.type : 'host'" @saved="changed" />
    <InboundDialog v-model:open="dialogs.inbound" :inbound="selected" :kind="selected ? selected.type : 'socks5'" :outbounds="outboundTargets"
      :ports="data ? data.ports : undefined" @saved="changed" />
    <RouteDialog v-model:open="dialogs.route" :route="selected" :targets="data ? data.targets : []" :directives="data ? data.directives : []" @saved="changed" />
    <FallbackDialog v-model:open="dialogs.fallback" :fallback="selected" :targets="data ? data.targets : []" @saved="changed" />
    <a-modal v-model:open="dialogs.match" title="Everything else" ok-text="Save" width="480px" :confirm-loading="busy === 'match'" destroy-on-close @ok="saveDefault">
      <a-form layout="vertical">
        <a-form-item label="Target of connections that no route matched (MATCH)">
          <a-select v-model:value="defaultTarget" show-search :options="targetOptions(data ? data.targets : [])" />
        </a-form-item>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { message, theme } from 'ant-design-vue'
import {
  ApartmentOutlined, ApiOutlined, AppstoreOutlined, ArrowRightOutlined, BranchesOutlined, CloudOutlined, CloudServerOutlined,
  ExpandOutlined, FullscreenExitOutlined, FullscreenOutlined, ZoomInOutlined, ZoomOutOutlined,
  DeploymentUnitOutlined, DesktopOutlined, EditOutlined, EyeOutlined, GlobalOutlined, LockOutlined, NodeIndexOutlined,
  PauseCircleOutlined, PlayCircleOutlined, QuestionOutlined, ReloadOutlined, SafetyCertificateOutlined, SendOutlined,
  StopOutlined, SyncOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, id } from '../api'
import { activeStates, targetOptions } from '../format'
import { buildGraph, layoutGraph, nodeHeight, related } from './topology'
import OutboundDialog from '../views/OutboundDialog.vue'
import InboundDialog from '../views/InboundDialog.vue'
import RouteDialog from '../views/RouteDialog.vue'
import FallbackDialog from '../views/FallbackDialog.vue'

const props = defineProps({ overview: { type: Object, required: true } })
const emit = defineEmits(['refresh'])

const typeIcons = { host: GlobalOutlined, socks5: SendOutlined, http: CloudServerOutlined, openvpn: LockOutlined, amneziawg: SafetyCertificateOutlined }
const kindIcons = { router: DeploymentUnitOutlined, route: NodeIndexOutlined, match: AppstoreOutlined, fallback: BranchesOutlined,
  internet: CloudOutlined, direct: ArrowRightOutlined, reject: StopOutlined, missing: QuestionOutlined }
const typeColors = { host: '#1677ff', socks5: '#722ed1', http: '#2f54eb', openvpn: '#fa8c16', amneziawg: '#eb2f96' }
const kindColors = { router: '#0e9f97', route: '#13a8a8', match: '#5b6b79', fallback: '#9254de', internet: '#3d6fd8', direct: '#7a8794',
  reject: '#e5484d', missing: '#e5484d', entry: '#0b7f8a' }
const { token } = theme.useToken()
const data = ref(null)
const viewport = ref()
const width = ref(1200)
const hovered = ref('')
const busy = ref('')
const selected = ref(null)
const defaultTarget = ref('DIRECT')
const dialogs = reactive({ outbound: false, inbound: false, route: false, fallback: false, match: false })
const expanded = ref(false)
const minimumZoom = 0.25
const maximumZoom = 2.5
const storedZoom = localStorage.getItem('proximity-map-zoom')
const fit = ref(!storedZoom || storedZoom === 'fit')
const scale = ref(fit.value ? 1 : Number(storedZoom) || 1)
const available = ref(600)
const panning = ref(false)
const showDisabled = ref(localStorage.getItem('proximity-map-disabled') !== 'off')
let timer = null
let observer = null

const palette = computed(() => ({
  '--map-surface': token.value.colorBgLayout, '--map-dots': token.value.colorBorder, '--node-surface': token.value.colorBgContainer,
  '--node-border': token.value.colorBorderSecondary, '--node-shadow': token.value.boxShadowTertiary, '--text': token.value.colorText,
  '--text-secondary': token.value.colorTextSecondary, '--good': token.value.colorSuccess, '--busy': token.value.colorInfo,
  '--warn': token.value.colorWarning, '--bad': token.value.colorError, '--off': token.value.colorTextQuaternary,
  '--accent': token.value.colorPrimary
}))
const graph = computed(() => data.value ? buildGraph(data.value, props.overview, showDisabled.value) : { nodes: new Map(), edges: [] })
const layout = computed(() => layoutGraph(graph.value, width.value))
const highlight = computed(() => hovered.value && graph.value.nodes.has(hovered.value) ? related(layout.value, hovered.value) : null)
const hasIncoming = computed(() => new Set(layout.value.edges.map((edge) => edge.to)))
const hasOutgoing = computed(() => new Set(layout.value.edges.map((edge) => edge.from)))
const outboundTargets = computed(() => data.value ? data.value.targets.filter((item) => item.kind === 'outbound') : [])

const fitScale = computed(() => Math.max(minimumZoom, Math.min(1, width.value / layout.value.width, available.value / layout.value.height)))
const zoom = computed(() => fit.value ? fitScale.value : scale.value)

watch(showDisabled, (value) => localStorage.setItem('proximity-map-disabled', value ? 'on' : 'off'))
watch([fit, scale], () => localStorage.setItem('proximity-map-zoom', fit.value ? 'fit' : String(scale.value)))
watch(expanded, () => nextTick(measure))

const faded = (key) => !!highlight.value && !highlight.value.has(key)
const iconOf = (node) => node.kind === 'entry' ? node.icon === 'host' ? DesktopOutlined : ApiOutlined
  : node.kind === 'outbound' || node.kind === 'inbound' ? typeIcons[node.type] : kindIcons[node.kind]
const colorOf = (node) => node.kind === 'outbound' || node.kind === 'inbound' ? typeColors[node.type] : kindColors[node.kind]
const editable = (node) => ['outbound', 'inbound', 'route', 'fallback', 'match'].includes(node.kind)
const controllable = (node) => ['outbound', 'inbound', 'router'].includes(node.kind)
const toggleable = (node) => canEdit() && ['outbound', 'inbound', 'route', 'fallback'].includes(node.kind) && !node.provided
const clickable = (node) => (canEdit() && editable(node)) || !!node.link
const startable = (node) => node.kind === 'router' ? !props.overview.router.running
  : !node.disabled && !activeStates.includes(node.record.status.state)
const stoppable = (node) => node.kind === 'router' ? !props.overview.router.stopped
  : !node.disabled && node.record.status.state !== 'stopped'

function clickHint(node) {
  if (canEdit() && editable(node)) return node.provided ? 'Click to view the route' : 'Click to edit the settings'
  return node.link ? 'Click to open Settings' : ''
}

async function load(quiet) {
  try {
    const [outbounds, inbounds, routes, fallbacks, targets] = await Promise.all([api.get('/api/outbounds', true),
      api.get('/api/inbounds', true), api.get('/api/routes', true), api.get('/api/fallbacks', true), api.get('/api/targets', true)])
    data.value = { outbounds: outbounds.outbounds, inbounds: inbounds.inbounds, ports: inbounds.ports, routes: routes.routes,
      warnings: routes.warnings, directives: routes.directives, defaultTarget: routes.default_target, fallbacks, targets }
    if (quiet !== true) {
      await nextTick()
      observe()
    }
  } catch (error) {
    if (quiet !== true) message.error(error.message)
  }
}

function observe() {
  if (observer || !viewport.value) return
  observer = new ResizeObserver(measure)
  observer.observe(viewport.value)
  measure()
}

function measure() {
  const element = viewport.value
  if (!element) return
  width.value = element.clientWidth
  available.value = expanded.value ? element.clientHeight
    : Math.max(360, Math.floor(window.innerHeight - element.getBoundingClientRect().top - 24))
}

function setZoom(value, clientX, clientY) {
  const element = viewport.value
  const next = Math.min(maximumZoom, Math.max(minimumZoom, value))
  if (!element || next === zoom.value) {
    fit.value = false
    scale.value = next
    return
  }
  const bounds = element.getBoundingClientRect()
  const offsetX = (clientX === undefined ? bounds.width / 2 : clientX - bounds.left)
  const offsetY = (clientY === undefined ? bounds.height / 2 : clientY - bounds.top)
  const sizer = element.firstElementChild
  const pointX = (element.scrollLeft + offsetX - sizer.offsetLeft) / zoom.value
  const pointY = (element.scrollTop + offsetY - sizer.offsetTop) / zoom.value
  fit.value = false
  scale.value = next
  nextTick(() => {
    element.scrollLeft = pointX * next + sizer.offsetLeft - offsetX
    element.scrollTop = pointY * next + sizer.offsetTop - offsetY
  })
}

const zoomBy = (factor) => setZoom(Math.round(zoom.value * factor * 100) / 100)

function fitToScreen() {
  fit.value = true
  nextTick(measure)
}

function wheel(event) {
  if (event.shiftKey) return
  event.preventDefault()
  hovered.value = ''
  setZoom(zoom.value * Math.exp(-event.deltaY * 0.0015), event.clientX, event.clientY)
}

function pan(event) {
  const element = viewport.value
  if (event.button !== 0 || event.target.closest('.node') || element.scrollWidth <= element.clientWidth && element.scrollHeight <= element.clientHeight) return
  const start = { x: event.clientX, y: event.clientY, left: element.scrollLeft, top: element.scrollTop }
  const move = (moved) => {
    panning.value = true
    element.scrollLeft = start.left - (moved.clientX - start.x)
    element.scrollTop = start.top - (moved.clientY - start.y)
  }
  const stop = () => {
    panning.value = false
    window.removeEventListener('mousemove', move)
    window.removeEventListener('mouseup', stop)
  }
  event.preventDefault()
  window.addEventListener('mousemove', move)
  window.addEventListener('mouseup', stop)
}

function strip(record) {
  const { status, traffic, usage, redacted, ...result } = record
  return result
}

function open(node) {
  if (canEdit() && editable(node)) {
    edit(node)
  } else if (node.link) {
    window.location.hash = `#/${node.link}`
  }
}

function edit(node) {
  hovered.value = ''
  if (node.kind === 'match') {
    defaultTarget.value = data.value.defaultTarget
  } else {
    selected.value = node.record
  }
  dialogs[node.kind] = true
}

async function changed() {
  await load(true)
  emit('refresh')
}

async function run(key, action, done) {
  busy.value = key
  try {
    await action()
    if (done) message.success(done)
    await changed()
    return true
  } catch (error) {
    message.error(error.message)
    return false
  } finally {
    busy.value = ''
  }
}

const paths = { outbound: 'outbounds', inbound: 'inbounds', route: 'routes', fallback: 'fallbacks' }
const controlResults = { start: 'Start requested', stop: 'Stopped', restart: 'Restart requested' }

function toggle(node, enabled) {
  const key = node.kind === 'route' ? node.record.key : node.record.id
  return run(node.id, () => api.put(`/api/${paths[node.kind]}/${id(key)}`, { ...strip(node.record), enabled }),
    `${node.title} ${enabled ? 'enabled' : 'disabled'}`)
}

function control(node, action) {
  const path = node.kind === 'router' ? `/api/router/${action}` : `/api/${paths[node.kind]}/${id(node.record.id)}/${action}`
  return run(`${node.id}:${action}`, () => api.post(path), controlResults[action])
}

async function saveDefault() {
  if (await run('match', () => api.put('/api/routes/default', { target: defaultTarget.value }), 'Default target changed')) {
    dialogs.match = false
  }
}

function expand(value) {
  expanded.value = value
  hovered.value = ''
  if (value && !document.fullscreenElement && document.documentElement.requestFullscreen) {
    document.documentElement.requestFullscreen().catch(() => {})
  } else if (!value && document.fullscreenElement) {
    document.exitFullscreen().catch(() => {})
  }
}

function fullscreenChanged() {
  if (!document.fullscreenElement) {
    expanded.value = false
  }
}

function keyPressed(event) {
  if (event.key === 'Escape' && expanded.value && !Object.values(dialogs).some(Boolean)) {
    expand(false)
  }
}

onMounted(async () => {
  document.addEventListener('fullscreenchange', fullscreenChanged)
  document.addEventListener('keydown', keyPressed, true)
  window.addEventListener('resize', measure)
  await load(false)
  timer = setInterval(() => load(true), 3000)
})

onUnmounted(() => {
  clearInterval(timer)
  document.removeEventListener('fullscreenchange', fullscreenChanged)
  document.removeEventListener('keydown', keyPressed, true)
  window.removeEventListener('resize', measure)
  if (expanded.value && document.fullscreenElement) document.exitFullscreen().catch(() => {})
  if (observer) observer.disconnect()
})
</script>

<style scoped>
.map-tools { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 12px; }
.legend { display: inline-flex; align-items: center; gap: 5px; opacity: .8; }
.swatch { width: 9px; height: 9px; border-radius: 50%; display: inline-block; }
.swatch.good { background: #52c41a; }
.swatch.busy { background: #1677ff; }
.swatch.warn { background: #faad14; }
.swatch.bad { background: #ff4d4f; }
.swatch.off { background: #bfbfbf; }
.legend-flow path { stroke: var(--accent); stroke-width: 2.4; stroke-dasharray: 5 6; animation: flow .9s linear infinite; }
.map-card :deep(.ant-card-body) { padding: 0; }
.map-viewport { position: relative; overflow: auto; border-radius: 0 0 8px 8px;
  background-color: var(--map-surface); background-image: radial-gradient(var(--map-dots) 1px, transparent 1px); background-size: 22px 22px; }
.map-sizer { position: relative; margin: 0 auto; }
.map { position: absolute; left: 0; top: 0; transform-origin: 0 0; }
.map-panning { cursor: grabbing; }
.map-panning .node { pointer-events: none; }
.zoom-value { min-width: 52px; font-variant-numeric: tabular-nums; }
.map-expanded { position: fixed; inset: 0; z-index: 999; display: flex; flex-direction: column; border-radius: 0; }
.map-expanded :deep(.ant-card-head) { flex: none; }
.map-expanded :deep(.ant-card-body) { flex: 1; min-height: 0; display: flex; }
.map-expanded .map-viewport { flex: 1; display: flex; overflow: auto; border-radius: 0; }
.map-expanded .map-sizer { flex: none; margin: auto; }
.column-title { position: absolute; top: 14px; text-align: center; font-size: 11px; font-weight: 600; letter-spacing: 1.2px;
  text-transform: uppercase; color: var(--text-secondary); }
.column-count { display: inline-block; min-width: 18px; margin-left: 4px; padding: 0 5px; border-radius: 9px; font-size: 10px;
  background: var(--node-border); color: var(--text-secondary); letter-spacing: 0; }
.edges { position: absolute; inset: 0; pointer-events: none; overflow: visible; }
.edge { transition: opacity .2s; }
.edge path { fill: none; stroke-linecap: round; }
.edge-base { stroke: var(--edge); opacity: .55; }
.edge-flow { stroke: var(--edge); stroke-dasharray: 6 9; animation: flow .9s linear infinite; }
.edge-direct .edge-base { stroke-dasharray: 2 5; opacity: .75; }
.edge-good { --edge: var(--accent); }
.edge-busy { --edge: var(--busy); }
.edge-warn { --edge: var(--warn); }
.edge-bad { --edge: var(--bad); }
.edge-off { --edge: var(--off); }
.edge-off .edge-base { stroke-dasharray: 4 5; }
.edge-faded { opacity: .08; }
.edge-label circle { fill: var(--node-surface); stroke: var(--edge); stroke-width: 1.4; }
.edge-label text { font-size: 10px; font-weight: 700; fill: var(--text); text-anchor: middle; dominant-baseline: central; }
.edge-label.preferred circle { fill: var(--edge); }
.edge-label.preferred text { fill: #fff; }
@keyframes flow { to { stroke-dashoffset: -15; } }
@media (prefers-reduced-motion: reduce) { .edge-flow, .legend-flow path, .node-busy .node-dot { animation: none; } }
.node { position: absolute; box-sizing: border-box; display: flex; align-items: center; gap: 9px; padding: 0 10px 0 10px;
  border: 1px solid var(--node-border); border-left: 4px solid var(--state); border-radius: 10px; background: var(--node-surface);
  color: var(--text); box-shadow: var(--node-shadow); transition: opacity .2s, transform .15s, box-shadow .15s; user-select: none; }
.node-clickable { cursor: pointer; }
.node-clickable:hover { transform: translateY(-2px); box-shadow: 0 6px 18px rgba(0, 0, 0, .18); }
.node-good { --state: var(--good); }
.node-busy { --state: var(--busy); }
.node-warn { --state: var(--warn); }
.node-bad { --state: var(--bad); }
.node-off { --state: var(--off); }
.node-info { --state: var(--accent); }
.node-disabled { opacity: .5; border-style: dashed; border-left-style: solid; }
.node-faded { opacity: .22; }
.node-active { box-shadow: 0 0 0 1px var(--state), var(--node-shadow); }
.node-icon { flex: none; width: 32px; height: 32px; border-radius: 9px; display: flex; align-items: center; justify-content: center;
  color: #fff; font-size: 17px; }
.node-text { flex: 1; min-width: 0; line-height: 1.3; }
.node-head { display: flex; align-items: center; gap: 5px; }
.node-title { flex: 0 1 auto; min-width: 0; font-weight: 600; font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.node-subtitle { font-size: 11.5px; color: var(--text-secondary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  font-family: var(--font-mono); }
.node-flag { margin-right: 4px; color: #d4a106; }
.node-tag { flex: none; padding: 0 4px; border-radius: 4px; font-size: 9.5px; font-weight: 500; background: rgba(250, 173, 20, .18); color: #d48806; }
.node-dot { flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--state); }
.node-busy .node-dot { animation: pulse 1.2s ease-in-out infinite; }
.node-metric { flex: none; margin-left: auto; font-size: 11px; color: var(--text-secondary); white-space: nowrap; }
.node-extra { font-size: 11px; color: var(--accent); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-numeric: tabular-nums; }
@keyframes pulse { 50% { opacity: .25; } }
.port { position: absolute; top: 50%; width: 9px; height: 9px; margin-top: -4.5px; border-radius: 50%; background: var(--node-surface);
  border: 2px solid var(--state); box-sizing: border-box; }
.port-in { left: -7px; }
.port-out { right: -5px; }
.popover-title { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.popover-body { min-width: 240px; }
.detail { display: flex; justify-content: space-between; gap: 16px; font-size: 12px; line-height: 1.8; }
.detail span:last-child { text-align: right; word-break: break-word; }
.detail-alert { margin-top: 8px; font-size: 12px; }
.detail-actions { margin-top: 10px; }
.detail-hint { margin-top: 8px; font-size: 11px; }
</style>
