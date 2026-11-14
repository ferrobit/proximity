<template>
  <div>
    <h2 class="pane-title"><FilterOutlined /> Filters</h2>
    <a-alert v-if="failure" type="error" show-icon style="margin-bottom: 12px" message="Router configuration is not applied" :description="failure" />
    <a-alert v-if="problems.length" type="warning" show-icon style="margin-bottom: 12px" message="Some filters are not active">
      <template #description><div v-for="line in problems" :key="line">{{ line }}</div></template>
    </a-alert>
    <a-alert v-if="warnings.length" type="warning" show-icon closable style="margin-bottom: 12px"
      :message="`Inconsistent filters: ${warnings.length} warning${warnings.length > 1 ? 's' : ''}`">
      <template #description>
        <div v-for="(warning, index) in shownWarnings" :key="index">{{ warning.message }}</div>
        <a v-if="warnings.length > 5" @click="allWarnings = !allWarnings">{{ allWarnings ? 'Show less' : `Show all ${warnings.length}` }}</a>
      </template>
    </a-alert>
    <div class="toolbar">
      <a-button v-if="canEdit()" type="primary" @click="open(null)"><template #icon><PlusOutlined /></template>Add filter</a-button>
      <a-button @click="load"><template #icon><ReloadOutlined /></template>Refresh</a-button>
      <a-button v-if="canEdit()" @click="showConfig"><template #icon><CodeOutlined /></template>Router configuration</a-button>
      <span class="spacer" />
      <span class="muted">Filters are checked from top to bottom, the first match wins.</span>
    </div>
    <a-table :columns="columns" :data-source="filters" row-key="key" size="middle" :pagination="false" :loading="loading"
      :row-class-name="(record) => warned.has(record.key) ? 'row-warned' : ''">
      <template #bodyCell="{ column, record, index }">
        <template v-if="column.key === 'order'">
          <a-button-group size="small">
            <a-button :disabled="!canEdit() || index === 0" @click="move(record, -1)"><template #icon><ArrowUpOutlined /></template></a-button>
            <a-button :disabled="!canEdit() || index === filters.length - 1" @click="move(record, 1)"><template #icon><ArrowDownOutlined /></template></a-button>
          </a-button-group>
        </template>
        <template v-else-if="column.key === 'number'">{{ index + 1 }}</template>
        <template v-else-if="column.key === 'enabled'">
          <a-switch size="small" :checked="record.enabled" :disabled="!canEdit()" @change="toggle(record, $event)" />
        </template>
        <template v-else-if="column.key === 'id'">
          <span>{{ record.id || '—' }}</span>
          <a-tooltip v-if="record.provided" title="Read-only routes provided by the VPN connection. Enable to route them to this endpoint.">
            <a-tag color="gold" style="margin-left: 6px"><LockOutlined /> provided</a-tag>
          </a-tooltip>
          <a-tooltip v-if="warned.has(record.key)" title="See warnings above"><WarningOutlined style="margin-left: 6px; color: #faad14" /></a-tooltip>
        </template>
        <template v-else-if="column.key === 'directive'"><a-tag color="blue">{{ record.directive }}</a-tag></template>
        <template v-else-if="column.key === 'params'">
          <div class="mono">
            <div v-for="line in record.params.slice(0, 4)" :key="line">{{ line }}</div>
            <a-tooltip v-if="record.params.length > 4">
              <template #title><div v-for="line in record.params.slice(4, 40)" :key="line">{{ line }}</div></template>
              <span class="muted">… and {{ record.params.length - 4 }} more</span>
            </a-tooltip>
          </div>
        </template>
        <template v-else-if="column.key === 'target'"><a-tag :color="targetColor(record.target)">{{ record.target }}</a-tag></template>
        <template v-else-if="column.key === 'actions'">
          <a-space v-if="canEdit()">
            <a-tooltip :title="record.provided ? 'View' : 'Edit'"><a-button size="small" @click="open(record)">
              <template #icon><EyeOutlined v-if="record.provided" /><EditOutlined v-else /></template></a-button></a-tooltip>
            <a-popconfirm v-if="!record.provided" title="Delete this filter?" @confirm="remove(record)">
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
    <a-modal v-model:open="visible" :title="dialogTitle" width="680px" :confirm-loading="busy" destroy-on-close
      :ok-button-props="{ disabled: readOnly }" ok-text="Save" @ok="save">
      <a-form ref="formRef" layout="vertical" :model="form">
        <a-row :gutter="16">
          <a-col :span="16"><a-form-item label="ID (optional name)" name="id" :rules="[{ max: 64, message: 'Up to 64 characters' }]">
            <a-input v-model:value="form.id" :disabled="readOnly" /></a-form-item></a-col>
          <a-col :span="8"><a-form-item label="Enabled"><a-switch v-model:checked="form.enabled" :disabled="readOnly" /></a-form-item></a-col>
          <a-col :span="12"><a-form-item label="Directive" name="directive" :rules="[{ required: true }]">
            <a-select v-model:value="form.directive" :disabled="readOnly" show-search :options="directives.map((value) => ({ value }))" @change="preview" /></a-form-item></a-col>
          <a-col :span="12"><a-form-item label="Target" name="target" :rules="[{ required: true, message: 'Select target' }]">
            <a-select v-model:value="form.target" :disabled="readOnly" show-search :options="targetOptions" @change="preview" /></a-form-item></a-col>
        </a-row>
        <a-form-item label="Rule parameters (one per line)" name="text" :rules="[{ required: true, message: 'Enter at least one parameter' }]" :extra="hints[form.directive]">
          <a-textarea v-model:value="form.text" class="mono" :rows="8" :disabled="readOnly" spellcheck="false" @change="preview" />
        </a-form-item>
        <a-alert v-if="previewError" type="error" show-icon :message="previewError" />
        <a-collapse v-else-if="previewLines.length" ghost>
          <a-collapse-panel key="lines" :header="`Router rules: ${previewLines.length}`">
            <pre class="mono log-view" style="max-height: 180px">{{ previewLines.join('\n') }}</pre>
          </a-collapse-panel>
        </a-collapse>
      </a-form>
    </a-modal>
    <a-modal v-model:open="configVisible" title="Generated router (mihomo) configuration" width="860px" :footer="null">
      <pre class="mono log-view">{{ configText }}</pre>
    </a-modal>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  ArrowDownOutlined, ArrowUpOutlined, CodeOutlined, DeleteOutlined, EditOutlined, EyeOutlined, FilterOutlined,
  LockOutlined, PlusOutlined, ReloadOutlined, RightOutlined, WarningOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, id } from '../api'

const hints = {
  'DOMAIN': 'Exact host name, for example: www.example.com',
  'DOMAIN-SUFFIX': 'Domain and all its subdomains, for example: example.com',
  'DOMAIN-KEYWORD': 'Part of the host name, for example: google',
  'DOMAIN-WILDCARD': 'Host name with * and ? wildcards, for example: *.cdn.example.com',
  'DOMAIN-REGEX': 'Regular expression for the host name, for example: ^ads\\d+\\.example\\.com$',
  'GEOSITE': 'Category of the geosite database, for example: youtube',
  'GEOIP': 'Country code of the destination IP, for example: RU',
  'IP-CIDR': 'IP, network or IP:port. Examples: 10.0.0.0/8, 1.2.3.4 (→ /32), 1.2.3.4:443 (→ IP and port rule)',
  'IP-ASN': 'Autonomous system number, for example: 13335',
  'SRC-IP-CIDR': 'Source address or network of the client, for example: 192.168.1.0/24',
  'DST-PORT': 'Destination port or range, for example: 443 or 8000-8100',
  'SRC-PORT': 'Source port or range',
  'NETWORK': 'tcp or udp',
  'PROCESS-NAME': 'Process name, works only with host network mode, for example: curl',
  'PROCESS-PATH': 'Full path of the process, works only with host network mode'
}
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
const filters = ref([])
const directives = ref([])
const warnings = ref([])
const problems = ref([])
const failure = ref('')
const targets = ref([])
const defaultTarget = ref('DIRECT')
const loading = ref(false)
const visible = ref(false)
const busy = ref(false)
const editing = ref(null)
const allWarnings = ref(false)
const formRef = ref()
const form = reactive({ id: '', directive: 'DOMAIN-SUFFIX', target: undefined, enabled: true, text: '' })
const previewLines = ref([])
const previewError = ref('')
const configVisible = ref(false)
const configText = ref('')
let previewTimer = null

const readOnly = computed(() => !!(editing.value && editing.value.provided))
const dialogTitle = computed(() => !editing.value ? 'Add filter' : readOnly.value ? 'Provided filter (read-only)' : 'Edit filter')
const shownWarnings = computed(() => allWarnings.value ? warnings.value : warnings.value.slice(0, 5))
const warned = computed(() => new Set(warnings.value.flatMap((item) => item.keys)))
const targetOptions = computed(() => [
  { label: 'Built-in', options: targets.value.filter((item) => item.kind === 'builtin').map(option) },
  { label: 'Endpoints', options: targets.value.filter((item) => item.kind === 'endpoint').map(option) },
  { label: 'Fallbacks', options: targets.value.filter((item) => item.kind === 'fallback').map(option) }
])

function option(item) {
  const names = { DIRECT: 'DIRECT (host default routing)', REJECT: 'REJECT (block)' }
  return { value: item.id, label: (names[item.id] || item.id) + (item.enabled ? '' : ' (disabled)') }
}

function targetColor(target) {
  const found = targets.value.find((item) => item.id === target)
  if (!found) return 'red'
  return found.kind === 'builtin' ? 'default' : found.kind === 'fallback' ? 'purple' : 'cyan'
}

async function load() {
  loading.value = true
  try {
    const data = await api.get('/api/filters')
    filters.value = data.filters
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
  Object.assign(form, record
    ? { id: record.id, directive: record.directive, target: record.target, enabled: record.enabled, text: record.params.join('\n') }
    : { id: '', directive: 'DOMAIN-SUFFIX', target: undefined, enabled: true, text: '' })
  previewLines.value = []
  previewError.value = ''
  visible.value = true
  if (record) preview()
}

const body = () => ({ id: form.id, directive: form.directive, target: form.target, enabled: form.enabled, params: form.text.split('\n') })

function preview() {
  clearTimeout(previewTimer)
  previewTimer = setTimeout(async () => {
    if (!form.text.trim()) {
      previewLines.value = []
      previewError.value = ''
      return
    }
    try {
      previewLines.value = (await api.post('/api/filters/preview', { ...body(), target: form.target || 'TARGET' })).lines
      previewError.value = ''
    } catch (error) {
      previewError.value = error.message
    }
  }, 350)
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

async function save() {
  await formRef.value.validate()
  busy.value = true
  const done = await act(() => editing.value
    ? api.put(`/api/filters/${id(editing.value.key)}`, body())
    : api.post('/api/filters', body()))
  busy.value = false
  visible.value = !done
}

const toggle = (record, enabled) => act(() => api.put(`/api/filters/${id(record.key)}`, { ...record, enabled }))
const move = (record, delta) => act(() => api.post(`/api/filters/${id(record.key)}/move`, { delta }))
const remove = (record) => act(() => api.remove(`/api/filters/${id(record.key)}`))
const setDefault = (target) => act(() => api.put('/api/filters/default', { target }))

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
