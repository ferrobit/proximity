<template>
  <div>
    <h2 class="pane-title"><SettingOutlined /> Settings</h2>
    <a-card size="small" style="max-width: 760px">
      <a-form ref="formRef" layout="vertical" :model="form" :disabled="!isAdmin()">
        <a-divider orientation="left" style="margin-top: 0"><DeploymentUnitOutlined /> Router</a-divider>
        <a-row :gutter="16">
          <a-col :span="8"><a-form-item label="Proxy port (HTTP + SOCKS5)" name="mixed_port" :rules="[{ required: true, type: 'number', min: 1, max: 65535, message: '1…65535' }]"
            :extra="session.host_network ? 'Listens on the host directly' : 'Must match the port published by Docker'"><a-input-number v-model:value="form.mixed_port" style="width: 100%" /></a-form-item></a-col>
          <a-col :span="8"><a-form-item label="Bind address" name="bind_address" :rules="[{ required: true, pattern: /^\*$|^(\d{1,3}\.){3}\d{1,3}$|^[0-9a-fA-F:]+$/, message: '* or IP address' }]"
            :extra="session.host_network ? 'Overridden by PROXIMITY_PROXY_BIND when it is set' : '* listens on all addresses'"><a-input v-model:value="form.bind_address" /></a-form-item></a-col>
          <a-col :span="8"><a-form-item label="Router log level" name="log_level">
            <a-select v-model:value="form.log_level" :options="['silent', 'error', 'warning', 'info', 'debug'].map((value) => ({ value }))" /></a-form-item></a-col>
        </a-row>
        <a-form-item :extra="session.host_network
          ? 'Linux host network: programs need no proxy settings, every outgoing connection is checked against the filters'
          : 'Available only when the container uses the host network (Linux, docker-compose.yml)'">
          <a-checkbox v-model:checked="form.capture" :disabled="!isAdmin() || !session.host_network">Route all outgoing connections of the host through Proximity</a-checkbox>
        </a-form-item>
        <a-form-item extra="Enable when the host has IPv6 connectivity, otherwise IPv6 traffic bypasses the filters">
          <a-checkbox v-model:checked="form.ipv6">IPv6</a-checkbox>
        </a-form-item>
        <a-form-item extra="Periodically opens a TCP connection through HOST endpoints and to proxy servers to detect their state">
          <a-checkbox v-model:checked="form.connectivity_checks">Enable connectivity checks</a-checkbox>
        </a-form-item>
        <a-row :gutter="16">
          <a-col :span="12"><a-form-item label="Probe target (host:port)" name="probe_target" :rules="[{ required: true, pattern: /^[^\s:]+:\d{1,5}$/, message: 'host:port expected' }]"
            extra="Used to check HOST endpoints"><a-input v-model:value="form.probe_target" :disabled="!isAdmin() || !form.connectivity_checks" /></a-form-item></a-col>
          <a-col :span="12"><a-form-item label="Check interval, seconds" name="check_interval" :rules="[{ required: true, type: 'number', min: 5, max: 3600, message: '5…3600' }]">
            <a-input-number v-model:value="form.check_interval" style="width: 100%" :disabled="!isAdmin() || !form.connectivity_checks" /></a-form-item></a-col>
        </a-row>
        <a-divider orientation="left"><SafetyOutlined /> Access</a-divider>
        <a-form-item><a-checkbox v-model:checked="form.anonymous_dashboard">Anonymous users can view the dashboard</a-checkbox></a-form-item>
        <a-button v-if="isAdmin()" type="primary" :loading="busy" @click="save"><template #icon><SaveOutlined /></template>Save settings</a-button>
        <a-alert v-else type="info" show-icon message="Only an administrator can change settings." />
      </a-form>
      <template v-if="canEdit()">
        <a-divider orientation="left"><DatabaseOutlined /> Backup &amp; Restore</a-divider>
        <p class="muted">
          The file contains endpoints (with passwords and VPN keys), fallbacks, filters and settings. User accounts are not included.
          <span v-if="!isAdmin()">Import by an operator keeps the current settings.</span>
        </p>
        <a-space wrap>
          <a-button :loading="exporting" @click="exportBackup"><template #icon><DownloadOutlined /></template>Export configuration</a-button>
          <a-upload :before-upload="importBackup" :show-upload-list="false" accept=".json,application/json">
            <a-button :loading="importing"><template #icon><UploadOutlined /></template>Import configuration</a-button>
          </a-upload>
        </a-space>
      </template>
    </a-card>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  DatabaseOutlined, DeploymentUnitOutlined, DownloadOutlined, SafetyOutlined, SaveOutlined, SettingOutlined, UploadOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, isAdmin, loadSession, session } from '../api'

const emit = defineEmits(['refresh'])
const formRef = ref()
const busy = ref(false)
const exporting = ref(false)
const importing = ref(false)
const form = reactive({
  mixed_port: 7899, bind_address: '*', default_target: 'DIRECT', log_level: 'warning', ipv6: false, anonymous_dashboard: true,
  connectivity_checks: true, probe_target: '1.1.1.1:443', check_interval: 30, capture: true
})

async function load() {
  try {
    Object.assign(form, await api.get('/api/settings'))
  } catch (error) {
    message.error(error.message)
  }
}

async function save() {
  await formRef.value.validate()
  busy.value = true
  try {
    const current = await api.get('/api/settings', true)
    await api.put('/api/settings', { ...form, default_target: current.default_target })
    await loadSession()
    message.success('Settings saved')
  } catch (error) {
    message.error(error.message)
  } finally {
    busy.value = false
  }
}

async function exportBackup() {
  exporting.value = true
  try {
    const data = await api.get('/api/backup', true)
    const stamp = new Date().toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15)
    const link = document.createElement('a')
    link.href = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }))
    link.download = `proximity-backup-${stamp}.json`
    link.click()
    URL.revokeObjectURL(link.href)
  } catch (error) {
    message.error(error.message)
  } finally {
    exporting.value = false
  }
}

function importBackup(file) {
  const reader = new FileReader()
  reader.onload = () => {
    let data = null
    try {
      data = JSON.parse(String(reader.result))
    } catch (error) {
      message.error('The file is not a valid JSON document')
      return
    }
    Modal.confirm({
      title: 'Import configuration?',
      content: `Current endpoints, fallbacks and filters will be replaced with the content of '${file.name}'.`,
      okText: 'Import',
      okType: 'danger',
      async onOk() {
        importing.value = true
        try {
          const result = await api.post('/api/backup', data)
          message.success(`Imported: ${result.endpoints} endpoints, ${result.fallbacks} fallbacks, ${result.filters} filters`)
          await loadSession()
          await load()
          emit('refresh')
        } catch (error) {
          message.error(error.message)
        } finally {
          importing.value = false
        }
      }
    })
  }
  reader.readAsText(file)
  return false
}

onMounted(load)
</script>
