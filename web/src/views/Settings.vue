<template>
  <div>
    <h2 class="pane-title"><SettingOutlined /> Settings</h2>
    <a-card size="small" style="max-width: 760px">
      <a-form ref="formRef" layout="vertical" :model="form" :disabled="!isAdmin()">
        <a-divider orientation="left" style="margin-top: 0"><DeploymentUnitOutlined /> Router</a-divider>
        <a-row :gutter="16">
          <a-col :span="8"><a-form-item label="Proxy port (HTTP + SOCKS5)" name="mixed_port" :rules="[{ required: true, type: 'number', min: 1, max: 65535, message: '1…65535' }]"
            extra="Must match the port published by Docker"><a-input-number v-model:value="form.mixed_port" style="width: 100%" /></a-form-item></a-col>
          <a-col :span="8"><a-form-item label="Bind address" name="bind_address" :rules="[{ required: true, pattern: /^\*$|^(\d{1,3}\.){3}\d{1,3}$|^[0-9a-fA-F:]+$/, message: '* or IP address' }]"
            extra="* listens on all addresses"><a-input v-model:value="form.bind_address" /></a-form-item></a-col>
          <a-col :span="8"><a-form-item label="Router log level" name="log_level">
            <a-select v-model:value="form.log_level" :options="['silent', 'error', 'warning', 'info', 'debug'].map((value) => ({ value }))" /></a-form-item></a-col>
          <a-col :span="8"><a-form-item label="IPv6"><a-switch v-model:checked="form.ipv6" /></a-form-item></a-col>
        </a-row>
        <a-divider orientation="left"><HeartOutlined /> Connectivity checks</a-divider>
        <a-row :gutter="16">
          <a-col :span="12"><a-form-item label="Probe target (host:port)" name="probe_target" :rules="[{ required: true, pattern: /^[^\s:]+:\d{1,5}$/, message: 'host:port expected' }]"
            extra="Used to check HOST endpoints"><a-input v-model:value="form.probe_target" /></a-form-item></a-col>
          <a-col :span="12"><a-form-item label="Check interval, seconds" name="check_interval" :rules="[{ required: true, type: 'number', min: 5, max: 3600, message: '5…3600' }]">
            <a-input-number v-model:value="form.check_interval" style="width: 100%" /></a-form-item></a-col>
        </a-row>
        <a-divider orientation="left"><SafetyOutlined /> Access</a-divider>
        <a-form-item label="Anonymous users can view the dashboard"><a-switch v-model:checked="form.anonymous_dashboard" /></a-form-item>
        <a-button v-if="isAdmin()" type="primary" :loading="busy" @click="save"><template #icon><SaveOutlined /></template>Save settings</a-button>
        <a-alert v-else type="info" show-icon message="Only an administrator can change settings." />
      </a-form>
    </a-card>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { DeploymentUnitOutlined, HeartOutlined, SafetyOutlined, SaveOutlined, SettingOutlined } from '@ant-design/icons-vue'
import { api, isAdmin, loadSession } from '../api'

const formRef = ref()
const busy = ref(false)
const form = reactive({ mixed_port: 7890, bind_address: '*', default_target: 'DIRECT', log_level: 'warning', ipv6: false, anonymous_dashboard: true, probe_target: '1.1.1.1:443', check_interval: 30 })

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

onMounted(load)
</script>
