<template>
  <div>
    <h2 class="pane-title"><SettingOutlined /> Settings</h2>
    <div>
      <h3 class="pane-subtitle"><PoweroffOutlined /> Router service</h3>
      <a-card size="small" class="settings-card">
        <div class="service">
          <div class="service-state">
            <a-badge :status="service.badge" />
            <b>{{ service.text }}</b>
            <span v-if="router && router.running && router.version" class="muted">mihomo {{ router.version }}</span>
            <span v-if="router && router.restarts" class="muted">· unexpected exits: {{ router.restarts }}</span>
          </div>
          <a-space v-if="canEdit()" wrap>
            <a-button type="primary" :disabled="!router || router.running || !!acting" :loading="acting === 'start'" @click="control('start')">
              <template #icon><PlayCircleOutlined /></template>Start</a-button>
            <a-popconfirm ok-text="Stop" ok-type="danger" :disabled="!router || router.stopped || !!acting" @confirm="control('stop')"
              title="Stop the router?" description="The proxy port, inbounds and VPN outbounds stop, host traffic is no longer captured.">
              <a-button danger :disabled="!router || router.stopped || !!acting" :loading="acting === 'stop'">
                <template #icon><PauseCircleOutlined /></template>Stop</a-button>
            </a-popconfirm>
            <a-button :disabled="!router || !!acting" :loading="acting === 'restart'" @click="control('restart')">
              <template #icon><ReloadOutlined /></template>Restart</a-button>
          </a-space>
        </div>
        <a-alert v-if="service.problem" type="error" show-icon :message="service.problem" class="service-alert" />
        <div class="service-autostart">
          <a-checkbox :checked="form.autostart" :disabled="!isAdmin() || saving" @change="setAutostart($event.target.checked)">Start the router when the container starts</a-checkbox>
          <div class="muted service-hint">The router starts only if it was running when the container stopped. When the option is off, the router stays stopped after a container restart.</div>
        </div>
      </a-card>
    </div>
    <div class="pane-section">
      <h3 class="pane-subtitle"><DeploymentUnitOutlined /> Router</h3>
      <a-card size="small" class="settings-card">
        <a-form ref="formRef" layout="vertical" :model="form" :disabled="!isAdmin()">
          <a-divider orientation="left" style="margin-top: 0"><ApiOutlined /> Proxy</a-divider>
          <a-row :gutter="16">
            <a-col :span="8"><a-form-item label="Proxy port (HTTP + SOCKS5)" name="mixed_port" :rules="[{ required: true, type: 'number', min: 1, max: 65535, message: '1…65535' }]"
              :extra="session.host_network ? 'Listens on the host directly' : 'Must match the port published by Docker'"><a-input-number v-model:value="form.mixed_port" style="width: 100%" /></a-form-item></a-col>
            <a-col :span="8"><a-form-item label="Bind address" name="bind_address" :rules="[{ required: true, pattern: /^\*$|^(\d{1,3}\.){3}\d{1,3}$|^[0-9a-fA-F:]+$/, message: '* or IP address' }]"
              :extra="session.host_network ? 'Overridden by PROXIMITY_PROXY_BIND when it is set' : '* listens on all addresses'"><a-input v-model:value="form.bind_address" /></a-form-item></a-col>
            <a-col :span="8"><a-form-item label="Router log level" name="log_level">
              <a-select v-model:value="form.log_level" :options="['silent', 'error', 'warning', 'info', 'debug'].map((value) => ({ value }))" /></a-form-item></a-col>
          </a-row>
          <a-form-item :extra="session.host_network
            ? 'Linux host network: programs need no proxy settings, every outgoing connection is checked against the routes'
            : 'Available only when the container uses the host network (Linux, docker-compose.yml)'">
            <a-checkbox v-model:checked="form.capture" :disabled="!isAdmin() || !session.host_network">Route all outgoing connections of the host through Proximity</a-checkbox>
          </a-form-item>
          <a-form-item extra="Ping cannot pass a proxy: it goes out directly only where the route is DIRECT or HOST and through the tunnel for OpenVPN and AmneziaWG routes, other addresses do not answer. Off: every ping of the host leaves directly, past the routes">
            <a-checkbox v-model:checked="form.ping_filter" :disabled="!isAdmin() || !session.host_network || !form.capture">Keep ping of the host on its routes</a-checkbox>
          </a-form-item>
          <a-form-item extra="Enable when the host has IPv6 connectivity, otherwise IPv6 traffic bypasses the routes">
            <a-checkbox v-model:checked="form.ipv6">IPv6</a-checkbox>
          </a-form-item>
          <a-divider orientation="left"><GlobalOutlined /> DNS</a-divider>
          <a-row :gutter="16">
            <a-col :span="14"><a-form-item label="Default DNS servers" name="dns_text" :rules="[{ validator: checkServers }]"
              :extra="`For the domains that no outbound with its own DNS servers takes, and for the names in the settings. ${serversHint}`">
              <a-textarea v-model:value="form.dns_text" :auto-size="{ minRows: 2, maxRows: 8 }" placeholder="Empty: the resolver of the system" spellcheck="false" /></a-form-item></a-col>
            <a-col :span="10"><a-form-item label="Send the queries through" extra="Host: directly. Router: the routes decide by the address of the server. An outbound or a fallback: through it">
              <a-select v-model:value="form.dns_exit" :options="exitOptions(targets)" :disabled="!isAdmin() || !form.dns_text.trim()" /></a-form-item></a-col>
          </a-row>
          <a-divider orientation="left"><HeartOutlined /> Connectivity checks</a-divider>
          <a-form-item extra="Periodically opens a TCP connection through HOST outbounds and to proxy servers to detect their state">
            <a-checkbox v-model:checked="form.connectivity_checks">Enable connectivity checks</a-checkbox>
          </a-form-item>
          <a-row :gutter="16">
            <a-col :span="12"><a-form-item label="Probe target (host:port)" name="probe_target" :rules="[{ required: true, pattern: /^[^\s:]+:\d{1,5}$/, message: 'host:port expected' }]"
              extra="Used to check HOST outbounds"><a-input v-model:value="form.probe_target" :disabled="!isAdmin() || !form.connectivity_checks" /></a-form-item></a-col>
            <a-col :span="12"><a-form-item label="Check interval, seconds" name="check_interval" :rules="[{ required: true, type: 'number', min: 5, max: 3600, message: '5…3600' }]">
              <a-input-number v-model:value="form.check_interval" style="width: 100%" :disabled="!isAdmin() || !form.connectivity_checks" /></a-form-item></a-col>
          </a-row>
          <a-divider orientation="left"><StopOutlined /> Unreachable routes</a-divider>
          <a-row :gutter="16">
            <a-col :span="12"><a-form-item label="Maximum count" name="unreachable_limit" :rules="[{ required: true, type: 'number', min: 1, max: 10000, message: '1…10000' }]"
              extra="When the list is full the oldest entries are removed"><a-input-number v-model:value="form.unreachable_limit" style="width: 100%" /></a-form-item></a-col>
            <a-col :span="12"><a-form-item label="Maximum lifetime" name="lifetime" :rules="[{ validator: checkLifetime }]"
              extra="Entries that did not repeat for this time are removed (1 minute … 365 days)">
              <a-input-group compact>
                <a-input-number v-model:value="form.lifetime" :min="1" style="width: 60%" />
                <a-select v-model:value="form.lifetime_unit" style="width: 40%" :options="lifetimeUnits" />
              </a-input-group>
            </a-form-item></a-col>
          </a-row>
          <a-button v-if="isAdmin()" type="primary" :loading="busy" @click="save"><template #icon><SaveOutlined /></template>Save settings</a-button>
          <a-alert v-else type="info" show-icon message="Only an administrator can change settings." />
        </a-form>
      </a-card>
    </div>
    <div class="pane-section">
      <h3 class="pane-subtitle"><AppstoreOutlined /> Miscellaneous</h3>
      <a-card size="small" class="settings-card">
        <a-form ref="miscRef" layout="vertical" :model="misc" :disabled="!isAdmin()">
          <a-divider orientation="left" style="margin-top: 0"><ClockCircleOutlined /> Time synchronization</a-divider>
          <div class="time-status">
            <a-badge :status="timeView.badge" :text="timeView.title" />
            <span class="muted">{{ time.message }}</span>
            <span class="muted" v-if="time.checked">· checked {{ ago(time.checked) }}</span>
            <a-button v-if="canEdit()" size="small" :disabled="false" :loading="checkingTime" @click="checkTime">
              <template #icon><SyncOutlined /></template>Check now</a-button>
          </div>
          <a-form-item extra="Proximity compares the host clock with NTP servers and never changes it. When the host clock is wrong, OpenVPN clients and servers run with Proximity's own time: certificates and one-time codes (TOTP) need exact time.">
            <a-checkbox :checked="!misc.time_manual" @change="misc.time_manual = !$event.target.checked">Synchronize automatically</a-checkbox>
          </a-form-item>
          <p v-if="!misc.time_manual" class="muted">NTP servers {{ defaultTimeServers.join(', ') }}; allowed deviation of the host clock 1 s; checked every hour.</p>
          <a-row v-else :gutter="16">
            <a-col :span="12"><a-form-item label="NTP servers" name="time_servers"
              :rules="[{ required: true, type: 'array', min: 1, max: 10, message: '1…10 servers' }]" extra="Host name or IP address, optionally with :port">
              <a-select v-model:value="misc.time_servers" mode="tags" :open="false" :token-separators="[',', ' ']" placeholder="time.example.com" /></a-form-item></a-col>
            <a-col :span="6"><a-form-item label="Allowed deviation, seconds" name="time_tolerance"
              :rules="[{ required: true, type: 'number', min: 1, max: 600, message: '1…600' }]" extra="A larger deviation switches to Proximity's own time">
              <a-input-number v-model:value="misc.time_tolerance" style="width: 100%" /></a-form-item></a-col>
            <a-col :span="6"><a-form-item label="Check interval, seconds" name="time_interval"
              :rules="[{ required: true, type: 'number', min: 60, max: 86400, message: '60…86400' }]">
              <a-input-number v-model:value="misc.time_interval" style="width: 100%" /></a-form-item></a-col>
          </a-row>
          <a-row :gutter="16">
            <a-col :span="12"><a-form-item label="Wait for synchronization, seconds" name="time_wait"
              :rules="[{ required: true, type: 'number', min: 5, max: 600, message: '5…600' }]"
              extra="OpenVPN starts only with synchronized time; after this wait the connection fails (5…600)">
              <a-input-number v-model:value="misc.time_wait" style="width: 100%" /></a-form-item></a-col>
          </a-row>
          <a-divider orientation="left"><FileTextOutlined /> Event log</a-divider>
          <a-row :gutter="16">
            <a-col :span="12"><a-form-item label="Lines to keep in normal mode" name="log_lines" :rules="logLineRules"
              extra="Log of a connection with Verbose off (10…100000)"><a-input-number v-model:value="misc.log_lines" style="width: 100%" /></a-form-item></a-col>
            <a-col :span="12"><a-form-item label="Lines to keep in Verbose mode" name="verbose_log_lines" :rules="logLineRules"
              extra="Log of a connection with Verbose on (10…100000)"><a-input-number v-model:value="misc.verbose_log_lines" style="width: 100%" /></a-form-item></a-col>
          </a-row>
          <p class="muted">The limits apply to the logs of outbounds and inbounds at once: older lines are removed, connections are not restarted.</p>
          <a-form-item name="clear_log_on_restart"
            extra="The log of an outbound or inbound is emptied when you press Start or Restart. Automatic reconnections, the restart of the router and changed settings keep the log">
            <a-checkbox v-model:checked="misc.clear_log_on_restart">Clear the log when a connection is started or restarted</a-checkbox>
          </a-form-item>
          <a-button v-if="isAdmin()" type="primary" :loading="savingMisc" @click="saveMisc"><template #icon><SaveOutlined /></template>Save settings</a-button>
        </a-form>
      </a-card>
    </div>
    <Users v-if="isAdmin()" @changed="reload" />
    <div v-if="canEdit()" class="pane-section">
      <h3 class="pane-subtitle"><DatabaseOutlined /> Backup and Restore</h3>
      <a-card size="small" class="settings-card">
        <p class="muted">
          The file contains outbounds, inbounds (with passwords, VPN keys and certificates), fallbacks, routes and settings. User accounts are not included.
          <span v-if="!isAdmin()">Import by an operator keeps the current settings.</span>
        </p>
        <a-space wrap>
          <a-button :loading="exporting" @click="exportBackup"><template #icon><DownloadOutlined /></template>Export configuration</a-button>
          <a-upload :before-upload="importBackup" :show-upload-list="false" accept=".json,application/json">
            <a-button :loading="importing"><template #icon><UploadOutlined /></template>Import configuration</a-button>
          </a-upload>
        </a-space>
      </a-card>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  ApiOutlined, AppstoreOutlined, ClockCircleOutlined, DatabaseOutlined, DeploymentUnitOutlined, DownloadOutlined, FileTextOutlined, GlobalOutlined, HeartOutlined, PauseCircleOutlined, PlayCircleOutlined,
  PoweroffOutlined, ReloadOutlined, SaveOutlined, SettingOutlined, StopOutlined, SyncOutlined, UploadOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, isAdmin, loadSession, session } from '../api'
import { ago } from '../format'
import { checkServers, exitOptions, serversHint, splitServers } from '../dns'
import Users from './Users.vue'

const props = defineProps({ overview: { type: Object, default: null } })
const emit = defineEmits(['refresh'])
const actionResults = { start: 'Router started', stop: 'Router stopped', restart: 'Router restarted' }
const lifetimeUnits = [
  { value: 60, label: 'minutes' },
  { value: 3600, label: 'hours' },
  { value: 86400, label: 'days' }
]
const logLineRules = [{ required: true, type: 'number', min: 10, max: 100000, message: '10…100000' }]
const formRef = ref()
const miscRef = ref()
const savingMisc = ref(false)
const busy = ref(false)
const exporting = ref(false)
const importing = ref(false)
const saving = ref(false)
const acting = ref('')
const router = computed(() => (props.overview ? props.overview.router : null))
const service = computed(() => {
  const state = router.value
  const failure = props.overview ? props.overview.failure : ''
  if (!state) return { badge: 'processing', text: 'Loading…', problem: '' }
  if (state.running && failure) return { badge: 'warning', text: 'Running, the configuration is not applied', problem: failure }
  if (state.running) return { badge: 'success', text: 'Running', problem: '' }
  if (state.stopped) return { badge: 'default', text: 'Stopped', problem: '' }
  return { badge: 'error', text: 'Not running', problem: failure || state.message }
})
const form = reactive({
  autostart: true,
  mixed_port: 7899, bind_address: '*', default_target: 'DIRECT', log_level: 'warning', ipv6: false, anonymous_dashboard: true,
  connectivity_checks: true, probe_target: '1.1.1.1:443', check_interval: 30, capture: true, ping_filter: true, unreachable_limit: 100,
  unreachable_lifetime: 86400, lifetime: 1, lifetime_unit: 86400, dns_text: '', dns_exit: '@host'
})
const targets = ref([])
const miscFields = ['log_lines', 'verbose_log_lines', 'clear_log_on_restart', 'time_manual', 'time_servers', 'time_tolerance', 'time_interval', 'time_wait']
const defaultTimeServers = ['time.cloudflare.com', 'time.google.com', 'pool.ntp.org']
const misc = reactive({
  log_lines: 300, verbose_log_lines: 3000, clear_log_on_restart: true, time_manual: false, time_servers: [...defaultTimeServers], time_tolerance: 1,
  time_interval: 3600, time_wait: 60
})
const time = ref({ state: 'checking', message: '', checked: 0 })
const checkingTime = ref(false)
const timeStates = {
  host: ['success', 'Host clock is used'],
  corrected: ['warning', "Proximity's own time is used"],
  unsynchronized: ['error', 'Time is not synchronized'],
  checking: ['processing', 'Checking time']
}
const timeView = computed(() => {
  const [badge, title] = timeStates[time.value.state] || timeStates.checking
  return { badge, title }
})

function splitLifetime(seconds) {
  const unit = [...lifetimeUnits].reverse().find((item) => seconds % item.value === 0) || lifetimeUnits[0]
  form.lifetime = Math.max(1, Math.round(seconds / unit.value))
  form.lifetime_unit = unit.value
}

async function checkLifetime() {
  const seconds = Number(form.lifetime) * form.lifetime_unit
  if (!Number.isInteger(seconds) || seconds < 60 || seconds > 31536000) {
    throw new Error('1 minute … 365 days')
  }
}

async function load() {
  try {
    const settings = await api.get('/api/settings')
    Object.assign(form, settings)
    form.dns_text = ((settings.dns || {}).servers || []).join('\n')
    form.dns_exit = (settings.dns || {}).exit || '@host'
    targets.value = await api.get('/api/targets', true)
    Object.assign(misc, Object.fromEntries(miscFields.map((key) => [key, Array.isArray(settings[key]) ? [...settings[key]] : settings[key]])))
    await loadTime()
    splitLifetime(form.unreachable_lifetime)
  } catch (error) {
    message.error(error.message)
  }
}

async function reload() {
  await loadSession()
  await load()
}

async function save() {
  await formRef.value.validate()
  busy.value = true
  try {
    const current = await api.get('/api/settings', true)
    const { lifetime, lifetime_unit: unit, dns_text: servers, dns_exit: exit, ...settings } = form
    await api.put('/api/settings', {
      ...settings, dns: { servers: splitServers(servers), exit: servers.trim() ? exit : '@host' },
      default_target: current.default_target, anonymous_dashboard: current.anonymous_dashboard,
      autostart: current.autostart, unreachable_lifetime: lifetime * unit,
      ...Object.fromEntries(miscFields.map((key) => [key, current[key]]))
    })
    await reload()
    emit('refresh')
    message.success('Settings saved')
  } catch (error) {
    message.error(error.message)
  } finally {
    busy.value = false
  }
}

async function loadTime() {
  try {
    time.value = await api.get('/api/time', true)
  } catch (error) {
    time.value = { state: 'checking', message: error.message, checked: 0 }
  }
}

async function checkTime() {
  checkingTime.value = true
  try {
    time.value = await api.post('/api/time/check')
  } catch (error) {
    message.error(error.message)
  } finally {
    checkingTime.value = false
  }
}

async function saveMisc() {
  await miscRef.value.validate()
  savingMisc.value = true
  try {
    const current = await api.get('/api/settings', true)
    await api.put('/api/settings', { ...current, ...Object.fromEntries(miscFields.map((key) => [key, misc[key]])) })
    await reload()
    message.success('Settings saved')
  } catch (error) {
    message.error(error.message)
  } finally {
    savingMisc.value = false
  }
}

async function control(action) {
  acting.value = action
  try {
    const result = await api.post(`/api/router/${action}`)
    if (result.failure) {
      message.error(result.failure)
    } else if (!result.completed) {
      message.warning('The router is still processing the command')
    } else {
      message.success(actionResults[action])
    }
    emit('refresh')
  } catch (error) {
    message.error(error.message)
  } finally {
    acting.value = ''
  }
}

async function setAutostart(enabled) {
  saving.value = true
  try {
    const current = await api.get('/api/settings', true)
    await api.put('/api/settings', { ...current, autostart: enabled })
    form.autostart = enabled
    message.success(enabled ? 'Autostart enabled' : 'Autostart disabled')
  } catch (error) {
    message.error(error.message)
  } finally {
    saving.value = false
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
      content: `Current outbounds, inbounds, fallbacks and routes will be replaced with the content of '${file.name}'.`,
      okText: 'Import',
      okType: 'danger',
      async onOk() {
        importing.value = true
        try {
          const result = await api.post('/api/backup', data)
          message.success(`Imported: ${result.outbounds} outbounds, ${result.inbounds} inbounds, ${result.fallbacks} fallbacks, ${result.routes} routes`)
          await reload()
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

<style scoped>
.service { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.service-state { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.service-alert { margin-top: 12px; }
.service-autostart { margin-top: 12px; }
.service-hint { margin: 4px 0 0 24px; font-size: 13px; }
.time-status { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 16px; }
</style>
