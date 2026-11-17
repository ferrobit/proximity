<template>
  <div>
    <h2 class="pane-title"><ExportOutlined /> Outbounds</h2>
    <div class="toolbar">
      <a-dropdown v-if="canEdit()">
        <a-button type="primary"><template #icon><PlusOutlined /></template>Add outbound <DownOutlined /></a-button>
        <template #overlay>
          <a-menu @click="open(null, $event.key)">
            <a-menu-item v-for="(label, kind) in typeLabels" :key="kind"><component :is="typeIcons[kind]" /> {{ label }}</a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
      <a-button @click="load"><template #icon><ReloadOutlined /></template>Refresh</a-button>
      <span class="spacer" />
      <span class="muted">Outbound is an outgoing connection: host uplink, proxy or VPN.</span>
    </div>
    <a-table :columns="columns" :data-source="outbounds" row-key="id" size="middle" :pagination="false" :loading="loading" :scroll="{ x: 980 }">
      <template #bodyCell="{ column, record, index }">
        <template v-if="column.key === 'order'">
          <a-button-group size="small">
            <a-button :disabled="!canEdit() || index === 0" @click="move(record, -1)"><template #icon><ArrowUpOutlined /></template></a-button>
            <a-button :disabled="!canEdit() || index === outbounds.length - 1" @click="move(record, 1)"><template #icon><ArrowDownOutlined /></template></a-button>
          </a-button-group>
        </template>
        <template v-else-if="column.key === 'id'">
          <b>{{ record.id }}</b>
          <div class="muted" v-if="!record.autostart">manual start</div>
        </template>
        <template v-else-if="column.key === 'type'">
          <a-tag :color="typeColors[record.type]"><component :is="typeIcons[record.type]" /> {{ typeLabels[record.type] }}</a-tag>
        </template>
        <template v-else-if="column.key === 'enabled'">
          <a-switch size="small" :checked="record.enabled" :disabled="!canEdit()" @change="toggle(record, $event)" />
        </template>
        <template v-else-if="column.key === 'statistics'">
          <a-tooltip :title="record.statistics ? 'Statistics is collected' : 'Statistics is off'">
            <BarChartOutlined :style="{ opacity: record.statistics ? 1 : 0.25 }" />
          </a-tooltip>
        </template>
        <template v-else-if="column.key === 'state'">
          <StateTag :status="record.status" />
          <div class="muted" v-if="record.status.latency >= 0">{{ record.status.latency }} ms</div>
        </template>
        <template v-else-if="column.key === 'details'">
          <span class="mono">{{ summary(record) }}</span>
          <div class="muted mono" v-if="record.status.address">{{ record.status.device }} · {{ record.status.address }}</div>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space v-if="canEdit()">
            <a-tooltip title="Edit"><a-button size="small" @click="open(record, record.type)"><template #icon><EditOutlined /></template></a-button></a-tooltip>
            <a-tooltip title="Start"><a-button size="small" :disabled="!record.enabled || activeStates.includes(record.status.state)"
              @click="control(record, 'start')"><template #icon><PlayCircleOutlined /></template></a-button></a-tooltip>
            <a-popconfirm ok-text="Stop" ok-type="danger" :title="`Stop outbound '${record.id}'?`" :disabled="!record.enabled || record.status.state === 'stopped'" @confirm="control(record, 'stop')">
              <a-tooltip title="Stop"><a-button size="small" :disabled="!record.enabled || record.status.state === 'stopped'">
                <template #icon><PauseCircleOutlined /></template></a-button></a-tooltip>
            </a-popconfirm>
            <a-tooltip title="Restart"><a-button size="small" :disabled="!record.enabled" @click="control(record, 'restart')"><template #icon><SyncOutlined /></template></a-button></a-tooltip>
            <a-tooltip title="Connection log"><a-button size="small" @click="showLog(record)"><template #icon><FileTextOutlined /></template></a-button></a-tooltip>
            <a-popconfirm :title="`Delete outbound '${record.id}'?`" @confirm="remove(record)">
              <a-button size="small" danger><template #icon><DeleteOutlined /></template></a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
    </a-table>
    <OutboundDialog v-model:open="visible" :outbound="current" :kind="kind" @saved="load" />
    <a-modal v-model:open="logVisible" :title="`Connection log: ${logTitle}`" width="900px" :footer="null" destroy-on-close>
      <LogView :path="`/api/outbounds/${id(logTitle)}/log`" :file-name="`outbound-${logTitle}`" />
    </a-modal>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  ArrowDownOutlined, ArrowUpOutlined, BarChartOutlined, CloudServerOutlined, DeleteOutlined, DownOutlined,
  EditOutlined, ExportOutlined, FileTextOutlined, GlobalOutlined, LockOutlined, PauseCircleOutlined, PlayCircleOutlined,
  PlusOutlined, ReloadOutlined, SafetyCertificateOutlined, SendOutlined, SyncOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, id } from '../api'
import { activeStates, typeColors, typeLabels } from '../format'
import StateTag from '../components/StateTag.vue'
import LogView from '../components/LogView.vue'
import OutboundDialog from './OutboundDialog.vue'

const typeIcons = { host: GlobalOutlined, socks5: SendOutlined, http: CloudServerOutlined, openvpn: LockOutlined, amneziawg: SafetyCertificateOutlined }
const columns = [
  { title: '', key: 'order', width: 90 },
  { title: 'ID', key: 'id' },
  { title: 'Type', key: 'type' },
  { title: 'Enabled', key: 'enabled', width: 80 },
  { title: '', key: 'statistics', width: 40 },
  { title: 'Status', key: 'state' },
  { title: 'Connection', key: 'details' },
  { title: '', key: 'actions', width: 250 }
]
const outbounds = ref([])
const loading = ref(false)
const visible = ref(false)
const current = ref(null)
const kind = ref('host')
const logVisible = ref(false)
const logTitle = ref('')
let timer = null

async function load(quiet) {
  loading.value = quiet !== true
  try {
    const data = await api.get('/api/outbounds', quiet === true)
    outbounds.value = data.outbounds
  } catch (error) {
    if (quiet !== true) message.error(error.message)
  } finally {
    loading.value = false
  }
}

function summary(record) {
  if (record.type === 'host') {
    return record.host.mode === 'interface' ? `${record.host.interface}${record.host.gateway ? ` via ${record.host.gateway}` : ''}` : 'default routing'
  } else if (record.type === 'socks5') {
    return `${record.socks5.server}:${record.socks5.port}`
  } else if (record.type === 'http') {
    return `${record.http.server}:${record.http.port}${record.http.single ? '' : ' (per protocol)'}`
  }
  return record.status.gateway ? `gateway ${record.status.gateway}` : record.status.device || ''
}

function open(record, type) {
  current.value = record
  kind.value = type
  visible.value = true
}

async function act(action, done) {
  try {
    await action()
    if (done) message.success(done)
    await load()
  } catch (error) {
    message.error(error.message)
  }
}

function strip(record) {
  const { status, traffic, usage, redacted, ...outbound } = record
  return outbound
}

const toggle = (record, enabled) => act(() => api.put(`/api/outbounds/${id(record.id)}`, { ...strip(record), enabled }))
const move = (record, delta) => act(() => api.post(`/api/outbounds/${id(record.id)}/move`, { delta }))
const remove = (record) => act(() => api.remove(`/api/outbounds/${id(record.id)}`))
const controlResults = { start: 'Start requested', stop: 'Stopped', restart: 'Restart requested' }
const control = (record, action) => act(() => api.post(`/api/outbounds/${id(record.id)}/${action}`), controlResults[action])

function showLog(record) {
  logTitle.value = record.id
  logVisible.value = true
}

onMounted(() => {
  load()
  timer = setInterval(() => load(true), 3000)
})
onUnmounted(() => clearInterval(timer))
</script>
