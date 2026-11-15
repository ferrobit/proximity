<template>
  <div class="fallbacks">
    <h3 class="pane-subtitle"><BranchesOutlined /> Fallbacks</h3>
    <div class="toolbar">
      <a-button v-if="canEdit()" type="primary" @click="open(null)"><template #icon><PlusOutlined /></template>Add fallback</a-button>
      <a-button @click="load"><template #icon><ReloadOutlined /></template>Refresh</a-button>
      <span class="spacer" />
      <span class="muted">A fallback is a route target that uses the first outbound in the list that is alive.</span>
    </div>
    <a-table :columns="columns" :data-source="fallbacks" row-key="id" size="middle" :pagination="false" :loading="loading">
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
    <a-modal v-model:open="visible" :title="editing ? `Edit fallback '${editing}'` : 'Add fallback'" :confirm-loading="busy" destroy-on-close width="560px" @ok="save">
      <a-form ref="formRef" layout="vertical" :model="form">
        <a-row :gutter="16">
          <a-col :span="16">
            <a-form-item label="ID" name="id" :rules="[{ pattern: idPattern, message: 'Letters, digits, space, _ . - (up to 48)' }]"
              :extra="editing ? 'Unique among outbounds and fallbacks' : 'Optional, allocated automatically when empty'">
              <a-input v-model:value="form.id" :placeholder="editing || 'Fallback N'" />
            </a-form-item>
          </a-col>
          <a-col :span="8">
            <a-form-item label="Enabled"><a-switch v-model:checked="form.enabled" /></a-form-item>
          </a-col>
        </a-row>
        <a-form-item label="Outbounds (in priority order)">
          <a-list size="small" bordered :data-source="form.outbounds" :locale="{ emptyText: 'Add at least one outbound' }">
            <template #renderItem="{ item, index }">
              <a-list-item>
                <span><b>{{ index + 1 }}.</b> {{ item }}</span>
                <template #actions>
                  <a-button size="small" type="text" :disabled="index === 0" @click="shift(index, -1)"><template #icon><ArrowUpOutlined /></template></a-button>
                  <a-button size="small" type="text" :disabled="index === form.outbounds.length - 1" @click="shift(index, 1)"><template #icon><ArrowDownOutlined /></template></a-button>
                  <a-button size="small" type="text" danger @click="form.outbounds.splice(index, 1)"><template #icon><CloseOutlined /></template></a-button>
                </template>
              </a-list-item>
            </template>
          </a-list>
          <a-select style="width: 100%; margin-top: 8px" placeholder="Add outbound…" :value="undefined" :options="available" @select="form.outbounds.push($event)" />
        </a-form-item>
        <a-form-item extra="On: the router requests the URL through every outbound. Off: the first outbound in good state is used.">
          <a-checkbox v-model:checked="form.health_check">Enable health check</a-checkbox>
        </a-form-item>
        <a-row :gutter="16" v-if="form.health_check">
          <a-col :span="16">
            <a-form-item label="Health check URL" name="url" :rules="[{ type: 'url', message: 'Enter http(s) URL' }]">
              <a-input v-model:value="form.url" />
            </a-form-item>
          </a-col>
          <a-col :span="8">
            <a-form-item label="Interval, s" name="interval" :rules="[{ type: 'number', min: 10, max: 3600, message: '10…3600' }]">
              <a-input-number v-model:value="form.interval" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  ArrowDownOutlined, ArrowUpOutlined, BranchesOutlined, CloseOutlined, DeleteOutlined, EditOutlined,
  PlusOutlined, ReloadOutlined, RightOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, id } from '../api'

const emit = defineEmits(['changed'])
const idPattern = /^$|^[A-Za-z0-9]([A-Za-z0-9 _.-]{0,46}[A-Za-z0-9_.-])?$/
const columns = [
  { title: '', key: 'order', width: 90 },
  { title: 'ID', key: 'id' },
  { title: 'Enabled', key: 'enabled', width: 90 },
  { title: 'Outbounds', key: 'outbounds' },
  { title: 'Health check', key: 'check' },
  { title: '', key: 'actions', width: 110 }
]
const fallbacks = ref([])
const targets = ref([])
const loading = ref(false)
const visible = ref(false)
const busy = ref(false)
const editing = ref('')
const formRef = ref()
const form = reactive({ id: '', enabled: true, outbounds: [], health_check: false, url: '', interval: 60 })

const available = computed(() => targets.value
  .filter((item) => item.kind === 'outbound' && !form.outbounds.includes(item.id))
  .map((item) => ({ value: item.id, label: item.enabled ? item.id : `${item.id} (disabled)` })))

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
  editing.value = record ? record.id : ''
  Object.assign(form, record
    ? { ...record, outbounds: [...record.outbounds] }
    : { id: '', enabled: true, outbounds: [], health_check: false, url: 'https://www.gstatic.com/generate_204', interval: 60 })
  visible.value = true
}

function shift(index, delta) {
  const [item] = form.outbounds.splice(index, 1)
  form.outbounds.splice(index + delta, 0, item)
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

async function save() {
  await formRef.value.validate()
  if (!form.outbounds.length) {
    message.warning('Add at least one outbound')
    return
  }
  busy.value = true
  const done = await act(() => editing.value
    ? api.put(`/api/fallbacks/${id(editing.value)}`, form)
    : api.post('/api/fallbacks', form))
  busy.value = false
  visible.value = !done
}

const toggle = (record, enabled) => act(() => api.put(`/api/fallbacks/${id(record.id)}`, { ...record, enabled }))
const move = (record, delta) => act(() => api.post(`/api/fallbacks/${id(record.id)}/move`, { delta }))
const remove = (record) => act(() => api.remove(`/api/fallbacks/${id(record.id)}`))

onMounted(load)
</script>
