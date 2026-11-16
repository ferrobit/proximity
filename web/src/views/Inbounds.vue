<template>
  <div>
    <h2 class="pane-title"><ImportOutlined /> Inbounds</h2>
    <div class="toolbar">
      <a-dropdown v-if="canEdit()">
        <a-button type="primary"><template #icon><PlusOutlined /></template>Add inbound <DownOutlined /></a-button>
        <template #overlay>
          <a-menu @click="open(null, $event.key)">
            <a-menu-item v-for="(label, kind) in inboundLabels" :key="kind"><component :is="typeIcons[kind]" /> {{ label }} Inbound</a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
      <a-button @click="load"><template #icon><ReloadOutlined /></template>Refresh</a-button>
      <span class="spacer" />
      <span class="muted">Inbound is an incoming connection point: clients connect to ports {{ ports[0] }}…{{ ports[1] }} and their traffic goes to the selected outbound.</span>
    </div>
    <a-table :columns="columns" :data-source="inbounds" row-key="id" size="middle" :pagination="false" :loading="loading" :scroll="{ x: 1080 }">
      <template #bodyCell="{ column, record, index }">
        <template v-if="column.key === 'order'">
          <a-button-group size="small">
            <a-button :disabled="!canEdit() || index === 0" @click="move(record, -1)"><template #icon><ArrowUpOutlined /></template></a-button>
            <a-button :disabled="!canEdit() || index === inbounds.length - 1" @click="move(record, 1)"><template #icon><ArrowDownOutlined /></template></a-button>
          </a-button-group>
        </template>
        <template v-else-if="column.key === 'id'">
          <b class="nowrap">{{ record.id }}</b>
          <div class="muted" v-if="record.name">{{ record.name }}</div>
        </template>
        <template v-else-if="column.key === 'type'">
          <a-tag :color="inboundColors[record.type]"><component :is="typeIcons[record.type]" /> {{ inboundLabels[record.type] }}</a-tag>
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
        <template v-else-if="column.key === 'port'">
          <span class="nowrap"><span class="mono">{{ record.port }}</span>
            <span class="muted"> / {{ record.type === 'openvpn' ? record.openvpn.protocol : record.socks5.udp ? 'tcp+udp' : 'tcp' }}</span></span>
        </template>
        <template v-else-if="column.key === 'outbound'">
          <a-tag :color="viaColor(record.outbound)">{{ viaLabel(record.outbound) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'state'">
          <StateTag :status="record.status" />
          <a-tooltip v-if="record.type === 'openvpn' && record.status.state === 'up'">
            <template #title>
              <div v-if="!record.status.clients.length">No clients are connected</div>
              <div v-for="client in record.status.clients" :key="client.address">{{ client.name }} · {{ client.virtual }} · {{ client.address }}</div>
            </template>
            <div class="muted nowrap"><TeamOutlined /> {{ record.status.clients.length }} connected</div>
          </a-tooltip>
          <div class="muted nowrap" v-else-if="record.statistics && record.status.state === 'up'"><SwapOutlined /> {{ record.traffic.connections }} connections</div>
        </template>
        <template v-else-if="column.key === 'access'">
          <a-tag v-if="record.anonymous" color="gold">anonymous</a-tag>
          <span class="muted nowrap">{{ record.users.filter((user) => user.enabled).length }} of {{ record.users.length }} users</span>
        </template>
        <template v-else-if="column.key === 'traffic'">
          <template v-if="record.statistics">
            <div class="nowrap"><ArrowUpOutlined /> {{ bytes(record.traffic.upload) }} <span class="muted">{{ rate(record.traffic.upload_rate) }}</span></div>
            <div class="nowrap"><ArrowDownOutlined /> {{ bytes(record.traffic.download) }} <span class="muted">{{ rate(record.traffic.download_rate) }}</span></div>
          </template>
          <span v-else class="muted">—</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space v-if="canEdit()">
            <a-tooltip title="Edit"><a-button size="small" @click="open(record, record.type)"><template #icon><EditOutlined /></template></a-button></a-tooltip>
            <a-tooltip title="Restart"><a-button size="small" :disabled="!record.enabled" @click="restart(record)"><template #icon><SyncOutlined /></template></a-button></a-tooltip>
            <a-tooltip title="Inbound log"><a-button size="small" @click="showLog(record)"><template #icon><FileTextOutlined /></template></a-button></a-tooltip>
            <a-popconfirm :title="`Delete inbound '${record.id}'?`" @confirm="remove(record)">
              <a-button size="small" danger><template #icon><DeleteOutlined /></template></a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
    </a-table>
    <InboundDialog v-model:open="visible" :inbound="current" :kind="kind" :groups="groups" :outbounds="outbounds" :ports="ports" @saved="load" />
    <a-modal v-model:open="logVisible" :title="`Inbound log: ${logTitle}`" width="900px" :footer="null" destroy-on-close>
      <LogView :path="`/api/inbounds/${id(logTitle)}/log`" />
    </a-modal>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  ArrowDownOutlined, ArrowUpOutlined, BarChartOutlined, DeleteOutlined, DownOutlined, EditOutlined, FileTextOutlined,
  ImportOutlined, LockOutlined, PlusOutlined, ReloadOutlined, SendOutlined, SwapOutlined, SyncOutlined, TeamOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, id } from '../api'
import { bytes, inboundColors, inboundLabels, rate } from '../format'
import StateTag from '../components/StateTag.vue'
import LogView from '../components/LogView.vue'
import InboundDialog from './InboundDialog.vue'

const typeIcons = { socks5: SendOutlined, openvpn: LockOutlined }
const columns = [
  { title: '', key: 'order', width: 90 },
  { title: 'ID / Name', key: 'id' },
  { title: 'Type', key: 'type' },
  { title: 'Group', key: 'group' },
  { title: 'Enabled', key: 'enabled', width: 80 },
  { title: '', key: 'statistics', width: 40 },
  { title: 'Port', key: 'port' },
  { title: 'Outbound', key: 'outbound' },
  { title: 'Status', key: 'state' },
  { title: 'Access', key: 'access' },
  { title: 'Traffic', key: 'traffic' },
  { title: '', key: 'actions', width: 170 }
]
const inbounds = ref([])
const groups = ref([])
const outbounds = ref([])
const ports = ref([7800, 7898])
const loading = ref(false)
const visible = ref(false)
const current = ref(null)
const kind = ref('socks5')
const logVisible = ref(false)
const logTitle = ref('')
let timer = null

async function load(quiet) {
  loading.value = quiet !== true
  try {
    const data = await api.get('/api/inbounds', quiet === true)
    inbounds.value = data.inbounds
    groups.value = data.groups
    ports.value = data.ports
    if (quiet !== true) {
      outbounds.value = (await api.get('/api/targets', true)).filter((item) => item.kind === 'outbound')
    }
  } catch (error) {
    if (quiet !== true) message.error(error.message)
  } finally {
    loading.value = false
  }
}

const viaLabel = (value) => value === '@host' ? 'Host' : value === '@router' ? 'Router' : value
const viaColor = (value) => value === '@host' ? 'blue' : value === '@router' ? 'green' : 'cyan'

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
  const { status, traffic, redacted, ...inbound } = record
  return inbound
}

const toggle = (record, enabled) => act(() => api.put(`/api/inbounds/${id(record.id)}`, { ...strip(record), enabled }))
const move = (record, delta) => act(() => api.post(`/api/inbounds/${id(record.id)}/move`, { delta }))
const remove = (record) => act(() => api.remove(`/api/inbounds/${id(record.id)}`))
const restart = (record) => act(() => api.post(`/api/inbounds/${id(record.id)}/restart`), 'Restart requested')

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
