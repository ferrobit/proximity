<template>
  <div>
    <h2 class="pane-title"><ToolOutlined /> Tools</h2>
    <h3 class="pane-subtitle"><AimOutlined /> Address check</h3>
    <a-card size="small" class="tool-card lookup-card">
      <p class="muted intro">A name or an address goes through the router like a real connection: which route takes it, which way it
        leaves, and what every outbound gets for it — the answers of its DNS servers and a TCP connection through it.</p>
      <form class="tool-form" @submit.prevent="lookup">
        <a-input v-model:value="lookupForm.host" class="host-input" placeholder="example.com, 10.0.0.5:22 or https://host/path" allow-clear>
          <template #prefix><GlobalOutlined class="muted" /></template>
        </a-input>
        <a-input-number v-model:value="lookupForm.port" :min="1" :max="65535" placeholder="port" class="port-input" />
        <a-button type="primary" html-type="submit" :loading="lookupBusy" :disabled="!lookupForm.host.trim()">
          <template #icon><SearchOutlined /></template>Check</a-button>
        <span class="recent">
          <a-tag v-for="item in recent" :key="item" class="recent-tag" @click="repeat(item)">{{ item }}</a-tag>
        </span>
      </form>
      <a-alert v-if="lookupError" type="error" show-icon :message="lookupError" />
      <template v-if="checked">
        <div class="route-path">
          <div class="step"><DesktopOutlined /><span>Client</span></div>
          <ArrowRightOutlined class="arrow" />
          <div class="step route" :class="{ missing: !checked.route.name }">
            <span class="step-label">Route</span>
            <b>{{ checked.route.name || 'unknown' }}</b>
            <span class="mono muted">{{ checked.route.rule || '—' }}</span>
          </div>
          <template v-for="(name, index) in checked.route.chain" :key="`${name}${index}`">
            <ArrowRightOutlined class="arrow" />
            <div class="step" :class="{ group: index < checked.route.chain.length - 1 }">
              <span class="step-label">{{ index < checked.route.chain.length - 1 ? 'Group' : 'Outbound' }}</span>
              <b>{{ name }}</b>
            </div>
          </template>
          <ArrowRightOutlined class="arrow" :class="checked.route.error ? 'bad' : 'good'" />
          <div class="step target" :class="checked.route.error ? 'bad' : 'good'">
            <span class="step-label">{{ checked.host }}:{{ checked.port }}</span>
            <b v-if="!checked.route.error"><CheckCircleFilled /> {{ checked.route.proxied ? 'accepted by the proxy' : 'connected' }} in
              {{ ms(checked.route.latency) }}</b>
            <b v-else><CloseCircleFilled /> {{ checked.route.error }}</b>
          </div>
        </div>
        <div class="router-answer">
          <span class="muted">Router DNS answer</span>
          <a-tag v-for="address in checked.addresses" :key="address" class="mono">{{ address }}</a-tag>
          <span v-if="checked.problem" class="bad">{{ checked.problem }}</span>
        </div>
        <a-table :columns="lookupColumns" :data-source="checked.outbounds" row-key="id" size="small" :pagination="false"
          :row-class-name="(record) => record.id === checked.route.target || checked.route.chain.includes(record.id) ? 'chosen-row' : ''">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'id'">
              <a-badge :status="toneBadges[stateTone(record.state)]" />
              <b>{{ record.id }}</b>
              <a-tag v-if="record.type !== 'direct'" :color="typeColors[record.type]" class="type-tag">{{ typeLabels[record.type] }}</a-tag>
              <a-tag v-if="checked.route.chain.includes(record.id)" color="green" class="type-tag"><StarFilled /> route</a-tag>
            </template>
            <template v-else-if="column.key === 'dns'">
              <span v-if="!record.resolvers.length" class="muted">address, nothing to resolve</span>
              <div v-for="item in record.resolvers" :key="item.server" class="resolver">
                <span class="mono muted server">{{ item.server }}</span>
                <template v-if="!item.error">
                  <a-tag v-for="address in item.addresses" :key="address" class="mono" :color="sameAnswer(address) ? '' : 'orange'">{{ address }}</a-tag>
                  <span class="muted">{{ ms(item.latency) }}</span>
                </template>
                <span v-else class="bad">{{ item.error }}</span>
              </div>
            </template>
            <template v-else-if="column.key === 'connect'">
              <div v-if="!record.connect.error" class="latency">
                <div class="latency-bar" :style="{ width: `${barWidth(record.connect.latency)}px` }" />
                <span>{{ ms(record.connect.latency) }}</span>
                <a-tooltip v-if="record.connect.proxied" title="The proxy accepted the connection and kept it open for 5 seconds (a mihomo exit retries a failed dial for about 3 seconds); it may still fail to reach the target later">
                  <span class="muted">accepted by the proxy</span>
                </a-tooltip>
              </div>
              <span v-else class="bad"><CloseCircleFilled /> {{ record.connect.error }}</span>
            </template>
          </template>
        </a-table>
        <p class="muted note">Orange addresses differ from the router answer. A server without a scheme is asked over TCP through
          its outbound; the outbound marked <StarFilled /> carries the connection of the route.</p>
      </template>
    </a-card>

    <h3 class="pane-subtitle"><WifiOutlined /> Ping</h3>
    <a-card size="small" class="tool-card ping-card">
      <p class="muted intro">An echo request leaves through every way out: ICMP through the host and the tunnels, a timed TCP
        connection through the proxies (they do not carry ICMP).</p>
      <form class="tool-form" @submit.prevent="ping">
        <a-input v-model:value="pingForm.host" class="host-input" placeholder="1.1.1.1 or a name" allow-clear>
          <template #prefix><AimOutlined class="muted" /></template>
        </a-input>
        <a-select v-model:value="pingForm.count" class="count-select" :options="[3, 5, 10, 20].map((value) => ({ value, label: `${value} requests` }))" />
        <a-input-number v-model:value="pingForm.port" :min="1" :max="65535" addon-before="TCP" class="port-input" />
        <a-button type="primary" html-type="submit" :loading="pingBusy" :disabled="!pingForm.host.trim()">
          <template #icon><SendOutlined /></template>Ping</a-button>
      </form>
      <a-alert v-if="pingError" type="error" show-icon :message="pingError" />
      <template v-if="pinged">
        <PingMap :result="pinged" />
        <div class="ping-legend muted">
          <span><i class="swatch good" /> all answered</span>
          <span><i class="swatch warn" /> some lost</span>
          <span><i class="swatch bad" /> no answer</span>
          <span v-if="pinged.capture && pinged.system">thick edge: the way of a ping from clients ({{ pinged.system }})</span>
          <span v-else-if="pinged.capture && pinged.blocked">clients' pings to this address are answered by nobody: its route goes to a proxy</span>
          <span v-else>host capture is off: clients' pings do not pass Proximity</span>
        </div>
        <a-table :columns="pingColumns" :data-source="pingRows" row-key="id" size="small" :pagination="false" class="ping-table">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'id'"><b>{{ record.id }}</b><span class="muted method">{{ record.method.toUpperCase() }}</span></template>
            <template v-else-if="column.key === 'error'"><span class="bad">{{ record.error }}</span></template>
          </template>
        </a-table>
      </template>
    </a-card>
  </div>
</template>

<script setup>
import { computed, reactive, ref } from 'vue'
import {
  AimOutlined, ArrowRightOutlined, CheckCircleFilled, CloseCircleFilled, DesktopOutlined, GlobalOutlined, SearchOutlined, SendOutlined,
  StarFilled, ToolOutlined, WifiOutlined
} from '@ant-design/icons-vue'
import { api } from '../api'
import { typeColors, typeLabels } from '../format'
import { stateTone, toneBadges } from '../states'
import PingMap from '../components/PingMap.vue'

const recentKey = 'proximity-tools-recent'
const lookupColumns = [
  { title: 'Outbound', key: 'id', width: 260 },
  { title: 'DNS of the outbound', key: 'dns' },
  { title: 'TCP connection', key: 'connect', width: 340 }
]
const pingColumns = [
  { title: 'Outbound', key: 'id' },
  { title: 'Sent', dataIndex: 'sent', align: 'right' },
  { title: 'Received', dataIndex: 'received', align: 'right' },
  { title: 'Loss', dataIndex: 'loss', align: 'right' },
  { title: 'Min', dataIndex: 'min', align: 'right' },
  { title: 'Average', dataIndex: 'average', align: 'right' },
  { title: 'Max', dataIndex: 'max', align: 'right' },
  { title: '', key: 'error' }
]

const lookupForm = reactive({ host: '', port: null })
const pingForm = reactive({ host: '', count: 5, port: 443 })
const lookupBusy = ref(false)
const pingBusy = ref(false)
const lookupError = ref('')
const pingError = ref('')
const checked = ref(null)
const pinged = ref(null)
const recent = ref(readRecent())

const slowest = computed(() => Math.max(1, ...(checked.value?.outbounds || []).map((item) => item.connect.error ? 0 : item.connect.latency)))
const pingRows = computed(() => (pinged.value?.outbounds || []).map((item) => {
  const times = item.replies.map((reply) => reply.time)
  const received = times.length
  return { id: item.id, method: item.method, sent: item.sent, received, loss: `${Math.round(((item.sent - received) / item.sent) * 100)}%`,
    min: received ? ms(Math.min(...times)) : '—', max: received ? ms(Math.max(...times)) : '—',
    average: received ? ms(times.reduce((sum, value) => sum + value, 0) / received) : '—', error: received ? '' : item.error }
}))

function readRecent() {
  try {
    return JSON.parse(localStorage.getItem(recentKey) || '[]').slice(0, 6)
  } catch (error) {
    return []
  }
}

function remember(text) {
  recent.value = [text, ...recent.value.filter((item) => item !== text)].slice(0, 6)
  try {
    localStorage.setItem(recentKey, JSON.stringify(recent.value))
  } catch (error) {
    recent.value = recent.value.slice()
  }
}

function ms(value) {
  if (value >= 100) return `${Math.round(value)} ms`
  return value >= 1 ? `${value.toFixed(1)} ms` : `${value.toFixed(2)} ms`
}

function barWidth(value) {
  return Math.max(4, (value / slowest.value) * 150)
}

function sameAnswer(address) {
  return !checked.value.addresses.length || checked.value.addresses.includes(address)
}

function repeat(text) {
  lookupForm.host = text
  lookupForm.port = null
  lookup()
}

async function lookup() {
  lookupBusy.value = true
  lookupError.value = ''
  try {
    checked.value = await api.post('/api/tools/lookup', { host: lookupForm.host.trim(), port: lookupForm.port || 0 })
    remember(lookupForm.port ? `${lookupForm.host.trim()}:${lookupForm.port}` : lookupForm.host.trim())
  } catch (error) {
    lookupError.value = error.message
  } finally {
    lookupBusy.value = false
  }
}

async function ping() {
  pingBusy.value = true
  pingError.value = ''
  try {
    pinged.value = await api.post('/api/tools/ping', { host: pingForm.host.trim(), count: pingForm.count, port: pingForm.port || 0 })
  } catch (error) {
    pingError.value = error.message
  } finally {
    pingBusy.value = false
  }
}
</script>

<style scoped>
.tool-card { margin-bottom: 24px; }
.intro { margin: 0 0 12px; max-width: 900px; }
.tool-form { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin-bottom: 16px; }
.host-input { width: 380px; }
.port-input { width: 150px; }
.count-select { width: 140px; }
.recent { display: flex; flex-wrap: wrap; gap: 4px; }
.recent-tag { cursor: pointer; }
.route-path { display: flex; align-items: stretch; flex-wrap: wrap; gap: 8px; margin: 4px 0 16px; }
.step { display: flex; flex-direction: column; justify-content: center; gap: 2px; padding: 8px 14px; border-radius: 10px; border: 1px solid var(--ant-color-border, #d9d9d9); min-width: 96px; }
.step:first-child { flex-direction: row; align-items: center; gap: 8px; }
.step.route { border-color: #2a78d6; }
.step.route.missing { border-style: dashed; }
.step.group { border-style: dashed; }
.step.good { border-color: #1baf7a; }
.step.bad { border-color: #e34948; }
.step-label { font-size: 11px; opacity: .6; }
.arrow { align-self: center; opacity: .5; }
.arrow.good { color: #1baf7a; opacity: 1; }
.arrow.bad { color: #e34948; opacity: 1; }
.good { color: #1baf7a; }
.bad { color: #e34948; }
.router-answer { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 12px; }
.type-tag { margin-left: 6px; }
.resolver { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; padding: 2px 0; }
.resolver .server { min-width: 180px; }
.latency { display: flex; align-items: center; gap: 8px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.method { margin-left: 8px; }
.latency-bar { height: 8px; border-radius: 0 4px 4px 0; background: #2a78d6; max-width: 150px; flex: none; }
.note { margin: 8px 0 0; font-size: 12px; }
.ping-legend { display: flex; flex-wrap: wrap; gap: 16px; margin: 8px 0 12px; font-size: 12px; }
.swatch { display: inline-block; width: 10px; height: 10px; border-radius: 2px; margin-right: 4px; vertical-align: -1px; }
.swatch.good { background: #1baf7a; }
.swatch.warn { background: #eda100; }
.swatch.bad { background: #e34948; }
.ping-table :deep(td) { font-variant-numeric: tabular-nums; }
:deep(.chosen-row) td { background: rgba(27, 175, 122, .08); }
</style>
