<template>
  <div>
    <h2 class="pane-title"><SwapOutlined /> Routes</h2>
    <a-alert v-if="failure" type="error" show-icon style="margin-bottom: 12px" message="Router configuration is not applied" :description="failure" />
    <a-alert v-if="problems.length" type="warning" show-icon style="margin-bottom: 12px" message="Some routes are not active">
      <template #description><div v-for="line in problems" :key="line">{{ line }}</div></template>
    </a-alert>
    <a-alert v-if="warnings.length" type="warning" show-icon closable style="margin-bottom: 12px"
      :message="`Inconsistent routes: ${warnings.length} warning${warnings.length > 1 ? 's' : ''}`">
      <template #description>
        <div v-for="(warning, index) in shownWarnings" :key="index">{{ warning.message }}</div>
        <a v-if="warnings.length > 5" @click="allWarnings = !allWarnings">{{ allWarnings ? 'Show less' : `Show all ${warnings.length}` }}</a>
      </template>
    </a-alert>
    <div class="toolbar">
      <a-button v-if="canEdit()" type="primary" @click="open(null)"><template #icon><PlusOutlined /></template>Add route</a-button>
      <a-button @click="load"><template #icon><ReloadOutlined /></template>Refresh</a-button>
      <a-button v-if="canEdit()" @click="showConfig"><template #icon><CodeOutlined /></template>Router configuration</a-button>
      <span class="spacer" />
      <span class="muted">Routes are checked from top to bottom, the first match wins.</span>
    </div>
    <a-table :columns="columns" :data-source="routes" row-key="key" size="middle" :pagination="false" :loading="loading"
      :row-class-name="(record) => warned.has(record.key) ? 'row-warned' : ''">
      <template #bodyCell="{ column, record, index }">
        <template v-if="column.key === 'order'">
          <a-button-group size="small">
            <a-button :disabled="!canEdit() || index === 0" @click="move(record, -1)"><template #icon><ArrowUpOutlined /></template></a-button>
            <a-button :disabled="!canEdit() || index === routes.length - 1" @click="move(record, 1)"><template #icon><ArrowDownOutlined /></template></a-button>
          </a-button-group>
        </template>
        <template v-else-if="column.key === 'number'">{{ index + 1 }}</template>
        <template v-else-if="column.key === 'enabled'">
          <a-switch size="small" :checked="record.enabled" :disabled="!canEdit()" @change="toggle(record, $event)" />
        </template>
        <template v-else-if="column.key === 'id'">
          <b>{{ record.id }}</b>
          <a-tooltip v-if="record.provided" title="Read-only routes provided by the VPN connection. Enable to send them to this outbound.">
            <a-tag color="gold" style="margin-left: 6px"><LockOutlined /> provided</a-tag>
          </a-tooltip>
          <a-tooltip v-if="warned.has(record.key)" title="See warnings above"><WarningOutlined style="margin-left: 6px; color: #faad14" /></a-tooltip>
        </template>
        <template v-else-if="column.key === 'directive'"><a-tag color="blue">{{ record.directive }}</a-tag></template>
        <template v-else-if="column.key === 'params'">
          <div class="mono">
            <div v-for="(line, index) in record.params.slice(0, 4)" :key="index" :class="{ muted: line.startsWith('#') }">{{ line }}</div>
            <a-tooltip v-if="record.params.length > 4">
              <template #title><div v-for="(line, index) in record.params.slice(4, 40)" :key="index">{{ line }}</div></template>
              <span class="muted">… and {{ record.params.length - 4 }} more</span>
            </a-tooltip>
          </div>
        </template>
        <template v-else-if="column.key === 'target'"><a-tag :color="targetColor(record.target)">{{ record.target }}</a-tag></template>
        <template v-else-if="column.key === 'actions'">
          <a-space v-if="canEdit()">
            <a-tooltip :title="record.provided ? 'View' : 'Edit'"><a-button size="small" @click="open(record)">
              <template #icon><EyeOutlined v-if="record.provided" /><EditOutlined v-else /></template></a-button></a-tooltip>
            <a-popconfirm v-if="!record.provided" title="Delete this route?" @confirm="remove(record)">
              <a-button size="small" danger><template #icon><DeleteOutlined /></template></a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
      <template #footer>
        <div class="chain">
          <b>Everything else</b><span class="mono muted">MATCH</span><RightOutlined class="muted" />
          <a-select :value="defaultTarget" style="min-width: 220px" :disabled="!canEdit()" :options="targetOptions" @change="setDefault" />
        </div>
      </template>
    </a-table>
    <Fallbacks @changed="load" />
    <RouteDialog v-model:open="visible" :route="editing" :targets="targets" :directives="directives" @saved="load" />
    <a-modal v-model:open="configVisible" title="Generated router (mihomo) configuration" width="860px" :footer="null">
      <pre class="mono log-view">{{ configText }}</pre>
    </a-modal>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  ArrowDownOutlined, ArrowUpOutlined, CodeOutlined, DeleteOutlined, EditOutlined, EyeOutlined, LockOutlined,
  PlusOutlined, ReloadOutlined, RightOutlined, SwapOutlined, WarningOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, id } from '../api'
import { targetOptions as groupTargets } from '../format'
import Fallbacks from './Fallbacks.vue'
import RouteDialog from './RouteDialog.vue'

const columns = [
  { title: '', key: 'order', width: 90 },
  { title: '#', key: 'number', width: 44 },
  { title: 'On', key: 'enabled', width: 60 },
  { title: 'ID', key: 'id' },
  { title: 'Directive', key: 'directive' },
  { title: 'Rule parameters', key: 'params' },
  { title: 'Target', key: 'target' },
  { title: '', key: 'actions', width: 100 }
]
const routes = ref([])
const directives = ref([])
const warnings = ref([])
const problems = ref([])
const failure = ref('')
const targets = ref([])
const defaultTarget = ref('DIRECT')
const loading = ref(false)
const visible = ref(false)
const editing = ref(null)
const allWarnings = ref(false)
const configVisible = ref(false)
const configText = ref('')

const shownWarnings = computed(() => allWarnings.value ? warnings.value : warnings.value.slice(0, 5))
const warned = computed(() => new Set(warnings.value.flatMap((item) => item.keys)))
const targetOptions = computed(() => groupTargets(targets.value))

function targetColor(target) {
  const found = targets.value.find((item) => item.id === target)
  if (!found) return 'red'
  return found.kind === 'builtin' ? 'default' : found.kind === 'fallback' ? 'purple' : 'cyan'
}

async function load() {
  loading.value = true
  try {
    const data = await api.get('/api/routes')
    routes.value = data.routes
    directives.value = data.directives
    warnings.value = data.warnings
    problems.value = data.problems
    failure.value = data.failure
    defaultTarget.value = data.default_target
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

async function act(action) {
  try {
    await action()
    await load()
    return true
  } catch (error) {
    message.error(error.message)
    return false
  }
}

const toggle = (record, enabled) => act(() => api.put(`/api/routes/${id(record.key)}`, { ...record, enabled }))
const move = (record, delta) => act(() => api.post(`/api/routes/${id(record.key)}/move`, { delta }))
const remove = (record) => act(() => api.remove(`/api/routes/${id(record.key)}`))
const setDefault = (target) => act(() => api.put('/api/routes/default', { target }))

async function showConfig() {
  try {
    configText.value = (await api.get('/api/router/config')).config
    configVisible.value = true
  } catch (error) {
    message.error(error.message)
  }
}

onMounted(load)
</script>

<style scoped>
:deep(.row-warned) td:first-child { box-shadow: inset 3px 0 0 #faad14; }
</style>
