<template>
  <div class="pane-section">
    <h3 class="pane-subtitle"><TeamOutlined /> Users</h3>
    <div class="toolbar">
      <a-button type="primary" @click="open(null)"><template #icon><UserAddOutlined /></template>Add user</a-button>
      <a-button @click="load"><template #icon><ReloadOutlined /></template>Refresh</a-button>
      <span class="spacer" />
      <a-checkbox :checked="session.anonymous_dashboard" :disabled="saving" @change="setAnonymous($event.target.checked)">Anonymous users can view the dashboard</a-checkbox>
    </div>
    <a-table :columns="columns" :data-source="users" row-key="name" size="middle" :pagination="false" :loading="loading">
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'"><UserOutlined /> <b>{{ record.name }}</b> <a-tag v-if="record.name === session.user">you</a-tag></template>
        <template v-else-if="column.key === 'role'"><a-tag :color="roleColors[record.role]">{{ record.role }}</a-tag></template>
        <template v-else-if="column.key === 'access'">{{ access[record.role] }}</template>
        <template v-else-if="column.key === 'actions'">
          <a-space>
            <a-tooltip title="Edit"><a-button size="small" @click="open(record)"><template #icon><EditOutlined /></template></a-button></a-tooltip>
            <a-popconfirm :title="`Delete user '${record.name}'?`" :disabled="record.name === session.user" @confirm="remove(record)">
              <a-button size="small" danger :disabled="record.name === session.user"><template #icon><DeleteOutlined /></template></a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
    </a-table>
    <a-modal v-model:open="visible" :title="editing ? `Edit user '${editing}'` : 'Add user'" :confirm-loading="busy" destroy-on-close @ok="save">
      <a-form ref="formRef" layout="vertical" :model="form">
        <a-form-item label="User name" name="name" :rules="[{ required: true, pattern: /^[a-z0-9][a-z0-9_.-]{0,31}$/, message: 'Lower-case letters, digits, _ . - (up to 32)' }]">
          <a-input v-model:value="form.name" :disabled="!!editing" autocomplete="off" />
        </a-form-item>
        <a-form-item label="Role" name="role" :rules="[{ required: true }]">
          <a-select v-model:value="form.role" :disabled="editing === session.user" :options="roles" />
        </a-form-item>
        <a-form-item :label="editing ? 'New password (empty: keep current)' : 'Password'" name="password"
          :rules="[{ required: !editing, min: 6, message: 'At least 6 characters' }]">
          <a-input-password v-model:value="form.password" autocomplete="new-password" />
        </a-form-item>
        <a-form-item label="Repeat password" name="confirm" :rules="[{ validator: checkConfirm }]">
          <a-input-password v-model:value="form.confirm" autocomplete="new-password" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { DeleteOutlined, EditOutlined, ReloadOutlined, TeamOutlined, UserAddOutlined, UserOutlined } from '@ant-design/icons-vue'
import { api, id, session } from '../api'

const roleColors = { administrator: 'red', operator: 'blue', user: 'green' }
const access = { administrator: 'All access', operator: 'Edit configuration', user: 'View only' }
const roles = [
  { value: 'administrator', label: 'Administrator — all access' },
  { value: 'operator', label: 'Operator — edit configuration' },
  { value: 'user', label: 'User — view only' }
]
const columns = [
  { title: 'User', key: 'name' },
  { title: 'Role', key: 'role' },
  { title: 'Access level', key: 'access' },
  { title: '', key: 'actions', width: 110 }
]
const emit = defineEmits(['changed'])
const users = ref([])
const loading = ref(false)
const saving = ref(false)
const visible = ref(false)
const busy = ref(false)
const editing = ref('')
const formRef = ref()
const form = reactive({ name: '', role: 'user', password: '', confirm: '' })

async function load() {
  loading.value = true
  try {
    users.value = await api.get('/api/users')
  } catch (error) {
    message.error(error.message)
  } finally {
    loading.value = false
  }
}

async function setAnonymous(allowed) {
  saving.value = true
  try {
    const current = await api.get('/api/settings', true)
    await api.put('/api/settings', { ...current, anonymous_dashboard: allowed })
    emit('changed')
  } catch (error) {
    message.error(error.message)
  } finally {
    saving.value = false
  }
}

function open(record) {
  editing.value = record ? record.name : ''
  Object.assign(form, { name: record ? record.name : '', role: record ? record.role : 'user', password: '', confirm: '' })
  visible.value = true
}

async function checkConfirm(rule, value) {
  if ((value || '') !== (form.password || '')) {
    throw new Error('Passwords do not match')
  }
}

async function save() {
  await formRef.value.validate()
  busy.value = true
  try {
    if (editing.value) {
      await api.put(`/api/users/${id(editing.value)}`, { role: form.role, password: form.password, confirm: form.confirm })
    } else {
      await api.post('/api/users', form)
    }
    visible.value = false
    await load()
  } catch (error) {
    message.error(error.message)
  } finally {
    busy.value = false
  }
}

async function remove(record) {
  try {
    await api.remove(`/api/users/${id(record.name)}`)
    await load()
  } catch (error) {
    message.error(error.message)
  }
}

onMounted(load)
</script>
