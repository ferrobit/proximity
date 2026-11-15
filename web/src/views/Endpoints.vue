<template>
  <div>
    <h2 class="pane-title"><ApiOutlined /> Endpoints</h2>
    <div class="toolbar">
      <a-dropdown v-if="canEdit()">
        <a-button type="primary"><template #icon><PlusOutlined /></template>Add endpoint <DownOutlined /></a-button>
        <template #overlay>
          <a-menu @click="open(null, $event.key)">
            <a-menu-item v-for="(label, kind) in typeLabels" :key="kind"><component :is="typeIcons[kind]" /> {{ label }}</a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
      <a-button @click="load"><template #icon><ReloadOutlined /></template>Refresh</a-button>
      <span class="spacer" />
      <span class="muted">Endpoint is an outgoing connection: host uplink, proxy or VPN.</span>
    </div>
    <a-table :columns="columns" :data-source="endpoints" row-key="id" size="middle" :pagination="false" :loading="loading" :scroll="{ x: 980 }">
      <template #bodyCell="{ column, record, index }">
        <template v-if="column.key === 'order'">
          <a-button-group size="small">
            <a-button :disabled="!canEdit() || index === 0" @click="move(record, -1)"><template #icon><ArrowUpOutlined /></template></a-button>
            <a-button :disabled="!canEdit() || index === endpoints.length - 1" @click="move(record, 1)"><template #icon><ArrowDownOutlined /></template></a-button>
          </a-button-group>
        </template>
        <template v-else-if="column.key === 'id'">
          <b>{{ record.id }}</b>
          <div class="muted" v-if="record.name">{{ record.name }}</div>
        </template>
        <template v-else-if="column.key === 'type'">
          <a-tag :color="typeColors[record.type]"><component :is="typeIcons[record.type]" /> {{ typeLabels[record.type] }}</a-tag>
        </template>
        <template v-else-if="column.key === 'group'">
          <a-tag v-if="record.group">{{ record.group }}</a-tag>
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
            <a-tooltip title="Reconnect"><a-button size="small" :disabled="!record.enabled" @click="restart(record)"><template #icon><SyncOutlined /></template></a-button></a-tooltip>
            <a-tooltip title="Connection log"><a-button size="small" @click="showLog(record)"><template #icon><FileTextOutlined /></template></a-button></a-tooltip>
            <a-popconfirm :title="`Delete endpoint '${record.id}'?`" @confirm="remove(record)">
              <a-button size="small" danger><template #icon><DeleteOutlined /></template></a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
    </a-table>
    <EndpointDialog v-model:open="visible" :endpoint="current" :kind="kind" :groups="groups" @saved="load" />
    <a-modal v-model:open="logVisible" :title="`Connection log: ${logTitle}`" width="900px" :footer="null" destroy-on-close>
      <LogView :path="`/api/endpoints/${id(logTitle)}/log`" />
    </a-modal>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  ApiOutlined, ArrowDownOutlined, ArrowUpOutlined, BarChartOutlined, CloudServerOutlined, DeleteOutlined,
  DownOutlined, EditOutlined, FileTextOutlined, GlobalOutlined, LockOutlined, PlusOutlined, ReloadOutlined,
  SafetyCertificateOutlined, SendOutlined, SyncOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, id } from '../api'
import { typeColors, typeLabels } from '../format'
import StateTag from '../components/StateTag.vue'
import LogView from '../components/LogView.vue'
import EndpointDialog from './EndpointDialog.vue'

const typeIcons = { host: GlobalOutlined, socks5: SendOutlined, http: CloudServerOutlined, openvpn: LockOutlined, amneziawg: SafetyCertificateOutlined }
const columns = [
  { title: '', key: 'order', width: 90 },
  { title: 'ID / Name', key: 'id' },
  { title: 'Type', key: 'type' },
  { title: 'Group', key: 'group' },
  { title: 'Enabled', key: 'enabled', width: 80 },
  { title: '', key: 'statistics', width: 40 },
  { title: 'Status', key: 'state' },
  { title: 'Connection', key: 'details' },
  { title: '', key: 'actions', width: 170 }
]
const endpoints = ref([])
const groups = ref([])
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
    const data = await api.get('/api/endpoints', quiet === true)
    endpoints.value = data.endpoints
    groups.value = data.groups
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
  const { status, traffic, usage, redacted, ...endpoint } = record
  return endpoint
}

const toggle = (record, enabled) => act(() => api.put(`/api/endpoints/${id(record.id)}`, { ...strip(record), enabled }))
const move = (record, delta) => act(() => api.post(`/api/endpoints/${id(record.id)}/move`, { delta }))
const remove = (record) => act(() => api.remove(`/api/endpoints/${id(record.id)}`))
const restart = (record) => act(() => api.post(`/api/endpoints/${id(record.id)}/restart`), 'Reconnect requested')

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
