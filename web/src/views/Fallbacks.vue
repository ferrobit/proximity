<template>
  <div class="fallbacks">
    <h3 class="pane-subtitle"><BranchesOutlined /> Fallbacks</h3>
    <div class="toolbar">
      <a-button v-if="canEdit()" type="primary" @click="open(null)"><template #icon><PlusOutlined /></template>Add fallback</a-button>
      <a-button @click="load"><template #icon><ReloadOutlined /></template>Refresh</a-button>
      <span class="spacer" />
      <span class="muted">A fallback is a route target that uses the first outbound in the list that is alive.</span>
    </div>
    <a-table :columns="columns" :data-source="fallbacks" row-key="id" size="middle" :pagination="false" :loading="loading"
      :row-class-name="(record) => rowTone(targets.length ? targetTone(targets, record.id) : 'good', record.enabled)">
      <template #bodyCell="{ column, record, index }">
        <template v-if="column.key === 'order'">
          <a-button-group size="small">
            <a-button :disabled="!canEdit() || index === 0" @click="move(record, -1)"><template #icon><ArrowUpOutlined /></template></a-button>
            <a-button :disabled="!canEdit() || index === fallbacks.length - 1" @click="move(record, 1)"><template #icon><ArrowDownOutlined /></template></a-button>
          </a-button-group>
        </template>
        <template v-else-if="column.key === 'id'"><b>{{ record.id }}</b></template>
        <template v-else-if="column.key === 'enabled'">
          <a-switch size="small" :checked="record.enabled" :disabled="!canEdit()" @change="toggle(record, $event)" />
        </template>
        <template v-else-if="column.key === 'outbounds'">
          <div class="chain">
            <template v-for="(member, position) in record.outbounds" :key="member">
              <RightOutlined v-if="position" class="muted" />
              <a-tag color="cyan">{{ member }}</a-tag>
            </template>
            <span v-if="!record.outbounds.length" class="muted">no outbounds</span>
          </div>
        </template>
        <template v-else-if="column.key === 'check'">
          <template v-if="record.health_check"><span class="mono">{{ record.url }}</span><span class="muted" style="margin-left: 8px">every {{ record.interval }}s</span></template>
          <span v-else class="muted">off · follows outbound state</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space v-if="canEdit()">
            <a-tooltip title="Edit"><a-button size="small" @click="open(record)"><template #icon><EditOutlined /></template></a-button></a-tooltip>
            <a-popconfirm :title="`Delete fallback '${record.id}'?`" @confirm="remove(record)">
              <a-button size="small" danger><template #icon><DeleteOutlined /></template></a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
    </a-table>
    <FallbackDialog v-model:open="visible" :fallback="editing" :targets="targets" @saved="changed" />
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  ArrowDownOutlined, ArrowUpOutlined, BranchesOutlined, DeleteOutlined, EditOutlined,
  PlusOutlined, ReloadOutlined, RightOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, id } from '../api'
import { rowTone, targetTone } from '../states'
import FallbackDialog from './FallbackDialog.vue'

const emit = defineEmits(['changed'])
const columns = [
  { title: '', key: 'order', width: 90 },
  { title: 'Enabled', key: 'enabled', width: 90 },
  { title: 'ID', key: 'id' },
  { title: 'Outbounds', key: 'outbounds' },
  { title: 'Health check', key: 'check' },
  { title: '', key: 'actions', width: 110 }
]
const fallbacks = ref([])
const targets = ref([])
const loading = ref(false)
const visible = ref(false)
const editing = ref(null)

async function load() {
  loading.value = true
  try {
    fallbacks.value = await api.get('/api/fallbacks')
    targets.value = await api.get('/api/targets', true)
  } catch (error) {
    message.error(error.message)
  } finally {
    loading.value = false
  }
}

function open(record) {
  editing.value = record
  visible.value = true
}

async function changed() {
  await load()
  emit('changed')
}

async function act(action) {
  try {
    await action()
    await load()
    emit('changed')
    return true
  } catch (error) {
    message.error(error.message)
    return false
  }
}

const toggle = (record, enabled) => act(() => api.put(`/api/fallbacks/${id(record.id)}`, { ...record, enabled }))
const move = (record, delta) => act(() => api.post(`/api/fallbacks/${id(record.id)}/move`, { delta }))
const remove = (record) => act(() => api.remove(`/api/fallbacks/${id(record.id)}`))

let timer = null

async function refreshTargets() {
  try {
    targets.value = await api.get('/api/targets', true)
  } catch (error) {
    clearInterval(timer)
  }
}

onMounted(() => {
  load()
  timer = setInterval(refreshTargets, 3000)
})
onUnmounted(() => clearInterval(timer))
</script>
