<template>
  <a-modal :open="open" :title="title" width="780px" :confirm-loading="busy" ok-text="Save" destroy-on-close
    @ok="save(false)" @cancel="$emit('update:open', false)">
    <a-form ref="formRef" layout="vertical" :model="form">
      <a-tabs v-model:active-key="tab">
        <a-tab-pane key="general" force-render>
          <template #tab><ProfileOutlined /> General</template>
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item label="ID" name="id" :rules="[{ pattern: idPattern, message: 'Letters, digits, space, _ . - (up to 48)' }]"
                :extra="saved ? 'Unique among inbounds' : 'Unique, allocated automatically when empty'">
                <a-input v-model:value="form.id" :placeholder="saved || `${inboundLabels[kind]} N`" />
              </a-form-item>
            </a-col>
            <a-col :span="6"><a-form-item label="Enabled"><a-switch v-model:checked="form.enabled" /></a-form-item></a-col>
            <a-col :span="6"><a-form-item label="Collect statistics"><a-switch v-model:checked="form.statistics" /></a-form-item></a-col>
            <a-col :span="12">
              <a-form-item label="Start automatically" extra="Not started again after a stop or a failure">
                <a-checkbox v-model:checked="form.autostart">When the router starts</a-checkbox>
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="Port number" name="port" :rules="[{ type: 'number', min: ports[0], max: ports[1], message: `${ports[0]}…${ports[1]}` }]"
                :extra="`Unique, ${ports[0]}…${ports[1]} (published by Docker); the first free port is allocated when empty`">
                <a-input-number v-model:value="form.port" style="width: 100%" placeholder="Allocated automatically" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="Outbound" name="outbound" :rules="[{ required: true, message: 'Select outbound' }]" extra="Where connections of this inbound are sent">
                <a-select v-model:value="form.outbound" show-search :options="outboundOptions" />
              </a-form-item>
            </a-col>
          </a-row>
        </a-tab-pane>
        <a-tab-pane key="type" force-render>
          <template #tab><ControlOutlined /> {{ inboundLabels[kind] }}</template>
          <template v-if="kind === 'socks5'">
            <a-row :gutter="16">
              <a-col :span="12">
                <a-form-item label="Listen address" :name="['socks5', 'listen']" :rules="listenRules" extra="* accepts connections on all addresses">
                  <a-input v-model:value="form.socks5.listen" placeholder="*" />
                </a-form-item>
              </a-col>
              <a-col :span="6"><a-form-item label="UDP relay"><a-switch v-model:checked="form.socks5.udp" /></a-form-item></a-col>
              <a-col :span="6"><a-form-item label="TLS"><a-switch v-model:checked="form.socks5.tls" @change="enableTLS" /></a-form-item></a-col>
            </a-row>
            <template v-if="form.socks5.tls">
              <a-alert type="info" show-icon style="margin-bottom: 16px" message="Clients must connect with TLS (SOCKS5 over TLS). The certificate is self-signed unless you replace it." />
              <a-form-item :name="['socks5', 'certificate']" :rules="pemRules">
                <template #label>TLS certificate <a-button type="link" size="small" :loading="generating === 'tls'" @click="regenerate('tls')"><ReloadOutlined /> Regenerate</a-button></template>
                <a-textarea v-model:value="form.socks5.certificate" class="mono" :rows="5" spellcheck="false" />
              </a-form-item>
              <a-form-item label="TLS private key" :name="['socks5', 'key']" :rules="pemRules">
                <a-textarea v-model:value="form.socks5.key" class="mono" :rows="4" spellcheck="false" />
              </a-form-item>
            </template>
          </template>
          <template v-else>
            <a-divider orientation="left" style="margin-top: 0"><CloudServerOutlined /> Server</a-divider>
            <a-row :gutter="16">
              <a-col :span="12">
                <a-form-item label="Public address" :name="['openvpn', 'server']" :rules="[{ pattern: hostPattern, message: 'Enter IP address or hostname' }]"
                  extra="Address of this host as clients see it; written to exported configurations">
                  <a-input v-model:value="form.openvpn.server" placeholder="vpn.example.com" />
                </a-form-item>
              </a-col>
              <a-col :span="6">
                <a-form-item label="Public port" :name="['openvpn', 'public_port']" :rules="[{ type: 'number', min: 1, max: 65535, message: '1…65535' }]" extra="Empty: inbound port">
                  <a-input-number v-model:value="form.openvpn.public_port" style="width: 100%" :placeholder="String(form.port || 'port')" />
                </a-form-item>
              </a-col>
              <a-col :span="6">
                <a-form-item label="Protocol">
                  <a-select v-model:value="form.openvpn.protocol" :options="[{ value: 'udp', label: 'UDP' }, { value: 'tcp', label: 'TCP' }]" />
                </a-form-item>
              </a-col>
              <a-col :span="12">
                <a-form-item label="Listen address" :name="['openvpn', 'listen']" :rules="listenRules" extra="* accepts connections on all addresses">
                  <a-input v-model:value="form.openvpn.listen" placeholder="*" />
                </a-form-item>
              </a-col>
              <a-col :span="12">
                <a-form-item label="VPN network" :name="['openvpn', 'network']" :rules="[{ pattern: /^$|^(\d{1,3}\.){3}\d{1,3}\/(1[6-9]|2\d)$/, message: 'IPv4 network /16…/29, for example 10.77.1.0/24' }]"
                  extra="Addresses of the server and clients inside the tunnel; allocated automatically when empty">
                  <a-input v-model:value="form.openvpn.network" placeholder="10.77.N.0/24" />
                </a-form-item>
              </a-col>
              <a-col :span="8"><a-form-item label="Maximum clients" :name="['openvpn', 'max_clients']" :rules="range(1, 1000)"><a-input-number v-model:value="form.openvpn.max_clients" style="width: 100%" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Tunnel MTU" :name="['openvpn', 'mtu']" :rules="range(1280, 1500)"><a-input-number v-model:value="form.openvpn.mtu" style="width: 100%" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Log verbosity" :name="['openvpn', 'verbosity']" :rules="range(1, 6)"><a-input-number v-model:value="form.openvpn.verbosity" style="width: 100%" /></a-form-item></a-col>
            </a-row>
            <a-divider orientation="left"><TeamOutlined /> Clients</a-divider>
            <a-row :gutter="16">
              <a-col :span="12"><a-form-item label="Route all client traffic through the VPN" extra="Pushes the default route to clients"><a-switch v-model:checked="form.openvpn.redirect_gateway" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item label="Clients can reach each other"><a-switch v-model:checked="form.openvpn.client_to_client" /></a-form-item></a-col>
              <a-col :span="12">
                <a-form-item label="Additional routes (one network per line)" :name="['openvpn', 'routes_text']" :rules="[{ validator: checkRoutes }]" extra="Pushed to clients, for example 192.168.10.0/24">
                  <a-textarea v-model:value="form.openvpn.routes_text" class="mono" :rows="3" spellcheck="false" />
                </a-form-item>
              </a-col>
              <a-col :span="12">
                <a-form-item label="DNS servers" :name="['openvpn', 'dns_text']" :rules="[{ pattern: /^$|^\s*(\d{1,3}\.){3}\d{1,3}(\s*[,\s]\s*(\d{1,3}\.){3}\d{1,3}){0,3}\s*$/, message: 'Up to 4 IPv4 addresses' }]"
                  extra="Empty: Proximity resolver, so that domain routes work for clients">
                  <a-input v-model:value="form.openvpn.dns_text" placeholder="Proximity resolver" />
                </a-form-item>
              </a-col>
            </a-row>
            <a-divider orientation="left"><SafetyOutlined /> Security</a-divider>
            <a-row :gutter="16">
              <a-col :span="24">
                <a-form-item label="Data ciphers" :name="['openvpn', 'ciphers']" :rules="[{ required: true, pattern: /^[A-Za-z0-9-]+(:[A-Za-z0-9-]+)*$/, message: 'Cipher names separated by colon' }]">
                  <a-input v-model:value="form.openvpn.ciphers" class="mono" />
                </a-form-item>
              </a-col>
              <a-col :span="8"><a-form-item label="HMAC digest"><a-select v-model:value="form.openvpn.digest" :options="['SHA256', 'SHA384', 'SHA512'].map((value) => ({ value }))" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Minimum TLS version"><a-select v-model:value="form.openvpn.tls_version" :options="['1.2', '1.3'].map((value) => ({ value }))" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Renegotiation, seconds" :name="['openvpn', 'renegotiation']" :rules="range(120, 86400)"><a-input-number v-model:value="form.openvpn.renegotiation" style="width: 100%" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Keepalive interval, seconds" :name="['openvpn', 'keepalive_interval']" :rules="range(1, 600)"><a-input-number v-model:value="form.openvpn.keepalive_interval" style="width: 100%" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Keepalive timeout, seconds" :name="['openvpn', 'keepalive_timeout']" :rules="[{ validator: checkTimeout }]"><a-input-number v-model:value="form.openvpn.keepalive_timeout" style="width: 100%" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Require one-time code (TOTP)" extra="Asked together with the password"><a-switch v-model:checked="form.openvpn.otp" /></a-form-item></a-col>
              <a-col :span="24">
                <a-form-item :name="['openvpn', 'otp_key']" :rules="[{ pattern: /^[A-Za-z2-7 =-]*$/, message: 'Base32 key expected' }]"
                  extra="Shared by all users of this inbound: add it to an authenticator application">
                  <template #label>TOTP key
                    <a-button type="link" size="small" :loading="generating === 'otp_key'" @click="regenerate('otp_key')"><ReloadOutlined /> Regenerate</a-button>
                    <a-button type="link" size="small" :disabled="!form.openvpn.otp_key" @click="copy(otpLink, 'Authenticator link copied')"><CopyOutlined /> Copy otpauth:// link</a-button>
                  </template>
                  <a-input v-model:value="form.openvpn.otp_key" class="mono" :disabled="!form.openvpn.otp" autocomplete="off" />
                </a-form-item>
              </a-col>
            </a-row>
            <a-divider orientation="left"><KeyOutlined /> Keys and certificates</a-divider>
            <a-alert type="info" show-icon style="margin-bottom: 16px">
              <template #message>
                Generated automatically. After regeneration clients need new configuration files.
                <a-button type="link" size="small" :loading="generating === 'pki'" @click="regenerate('pki')"><ReloadOutlined /> Regenerate CA and all certificates</a-button>
              </template>
            </a-alert>
            <a-row :gutter="16">
              <a-col :span="12"><a-form-item label="CA certificate" :name="['openvpn', 'ca_certificate']" :rules="pemRules"><a-textarea v-model:value="form.openvpn.ca_certificate" class="mono" :rows="4" spellcheck="false" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item label="CA private key" :name="['openvpn', 'ca_key']" extra="Needed only to issue certificates"><a-textarea v-model:value="form.openvpn.ca_key" class="mono" :rows="4" spellcheck="false" /></a-form-item></a-col>
              <a-col :span="12">
                <a-form-item :name="['openvpn', 'certificate']" :rules="pemRules">
                  <template #label>Server certificate <a-button type="link" size="small" :loading="generating === 'server'" @click="regenerate('server')"><ReloadOutlined /> Regenerate</a-button></template>
                  <a-textarea v-model:value="form.openvpn.certificate" class="mono" :rows="4" spellcheck="false" />
                </a-form-item>
              </a-col>
              <a-col :span="12"><a-form-item label="Server private key" :name="['openvpn', 'key']" :rules="pemRules"><a-textarea v-model:value="form.openvpn.key" class="mono" :rows="4" spellcheck="false" /></a-form-item></a-col>
              <a-col :span="12">
                <a-form-item :name="['openvpn', 'client_certificate']" :rules="pemRules">
                  <template #label>Client certificate <a-button type="link" size="small" :loading="generating === 'client'" @click="regenerate('client')"><ReloadOutlined /> Regenerate</a-button></template>
                  <a-textarea v-model:value="form.openvpn.client_certificate" class="mono" :rows="4" spellcheck="false" />
                </a-form-item>
              </a-col>
              <a-col :span="12"><a-form-item label="Client private key" :name="['openvpn', 'client_key']" :rules="pemRules"><a-textarea v-model:value="form.openvpn.client_key" class="mono" :rows="4" spellcheck="false" /></a-form-item></a-col>
              <a-col :span="24">
                <a-form-item :name="['openvpn', 'tls_crypt']" :rules="pemRules">
                  <template #label>TLS-crypt key <a-button type="link" size="small" :loading="generating === 'tls_crypt'" @click="regenerate('tls_crypt')"><ReloadOutlined /> Regenerate</a-button></template>
                  <a-textarea v-model:value="form.openvpn.tls_crypt" class="mono" :rows="4" spellcheck="false" />
                </a-form-item>
              </a-col>
            </a-row>
          </template>
        </a-tab-pane>
        <a-tab-pane key="users" force-render>
          <template #tab><TeamOutlined /> Users</template>
          <div class="toolbar">
            <a-checkbox v-model:checked="form.anonymous">Allow anonymous connections</a-checkbox>
            <a-button v-if="kind === 'openvpn' && form.anonymous" size="small" @click="exportUser('')"><template #icon><ExportOutlined /></template>Export anonymous configuration</a-button>
            <span class="spacer" />
            <a-button type="primary" size="small" @click="addUser"><template #icon><UserAddOutlined /></template>Add user</a-button>
          </div>
          <a-alert v-if="form.anonymous" type="warning" show-icon style="margin-bottom: 12px" :message="kind === 'socks5'
            ? 'Anyone who can reach the port can use this proxy: the user list is not checked.'
            : 'Clients with the certificate connect without user name and password; users from the list still need a valid password.'" />
          <a-alert v-else-if="!form.users.some((user) => user.enabled)" type="warning" show-icon style="margin-bottom: 12px"
            message="Nobody can connect: allow anonymous connections or add an enabled user." />
          <a-table :columns="userColumns" :data-source="form.users" :row-key="(record) => form.users.indexOf(record)" size="small" :pagination="false"
            :locale="{ emptyText: 'No users' }">
            <template #bodyCell="{ column, record, index }">
              <template v-if="column.key === 'enabled'"><a-switch v-model:checked="record.enabled" size="small" /></template>
              <template v-else-if="column.key === 'name'">
                <a-input v-model:value="record.name" :status="userError(record, index).name ? 'error' : ''" placeholder="User name" autocomplete="off" />
              </template>
              <template v-else-if="column.key === 'password'">
                <a-input-group compact>
                  <a-input-password v-model:value="record.password" :status="userError(record, index).password ? 'error' : ''" style="width: calc(100% - 32px)"
                    placeholder="Password" autocomplete="new-password" />
                  <a-tooltip title="Generate password"><a-button @click="record.password = randomPassword()"><template #icon><ThunderboltOutlined /></template></a-button></a-tooltip>
                </a-input-group>
              </template>
              <template v-else-if="column.key === 'actions'">
                <a-space>
                  <a-tooltip v-if="kind === 'openvpn'" title="Export configuration"><a-button size="small" :disabled="!record.enabled" @click="exportUser(record.name)"><template #icon><ExportOutlined /></template></a-button></a-tooltip>
                  <a-tooltip title="Delete user"><a-button size="small" danger @click="form.users.splice(index, 1)"><template #icon><DeleteOutlined /></template></a-button></a-tooltip>
                </a-space>
              </template>
            </template>
          </a-table>
          <p class="muted" style="margin-top: 12px">User name: letters, digits, _ . @ - (up to 64). Password: up to 64 printable characters without spaces.</p>
        </a-tab-pane>
      </a-tabs>
    </a-form>
    <a-modal v-model:open="exportVisible" :title="`Client configuration: ${exported.file}`" width="760px" destroy-on-close>
      <a-alert v-if="exported.notice" type="info" show-icon style="margin-bottom: 12px" :message="exported.notice" />
      <a-textarea :value="exported.config" class="mono" :rows="18" readonly spellcheck="false" />
      <a-checkbox v-if="exported.user" v-model:checked="exported.embed" style="margin-top: 12px" @change="fetchExport">Embed user name and password into the file (OpenVPN 2.6 or newer)</a-checkbox>
      <template #footer>
        <a-button @click="exportVisible = false">Close</a-button>
        <a-button @click="copy(exported.config, 'Configuration copied')"><template #icon><CopyOutlined /></template>Copy to clipboard</a-button>
        <a-button type="primary" @click="download"><template #icon><DownloadOutlined /></template>Export</a-button>
      </template>
    </a-modal>
  </a-modal>
</template>

<script setup>
import { computed, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import {
  CloudServerOutlined, ControlOutlined, CopyOutlined, DeleteOutlined, DownloadOutlined, ExportOutlined, KeyOutlined, ProfileOutlined,
  ReloadOutlined, SafetyOutlined, TeamOutlined, ThunderboltOutlined, UserAddOutlined
} from '@ant-design/icons-vue'
import { api, id } from '../api'
import { inboundLabels } from '../format'

const props = defineProps({
  open: Boolean,
  inbound: { type: Object, default: null },
  kind: { type: String, default: 'socks5' },
  outbounds: { type: Array, default: () => [] },
  ports: { type: Array, default: () => [7800, 7898] }
})
const emit = defineEmits(['update:open', 'saved'])

const idPattern = /^$|^[A-Za-z0-9]([A-Za-z0-9 _.-]{0,46}[A-Za-z0-9_.-])?$/
const hostPattern = /^$|^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$/
const namePattern = /^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$/
const passwordPattern = /^[!-~]{1,64}$/
const networkPattern = /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/
const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789'
const listenRules = [{ required: true, pattern: /^\*$|^(\d{1,3}\.){3}\d{1,3}$/, message: '* or IPv4 address' }]
const pemRules = [{ required: true, pattern: /-----BEGIN [A-Za-z0-9 ]+-----/, message: 'PEM text expected; press Regenerate to create it' }]
const userColumns = [
  { title: 'Enabled', key: 'enabled', width: 80 },
  { title: 'User name', key: 'name' },
  { title: 'Password', key: 'password' },
  { title: '', key: 'actions', width: 90 }
]
const range = (min, max) => [{ required: true, type: 'number', min, max, message: `${min}…${max}` }]

const formRef = ref()
const tab = ref('general')
const busy = ref(false)
const generating = ref('')
const saved = ref('')
const exportVisible = ref(false)
const exported = reactive({ user: '', file: '', config: '', embed: false, notice: '' })
const form = reactive({})

const title = computed(() => saved.value ? `Edit ${inboundLabels[props.kind]} inbound '${saved.value}'` : `Add ${inboundLabels[props.kind]} inbound`)
const outboundOptions = computed(() => [
  { value: '@router', label: 'Router — routes decide' },
  { value: '@host', label: 'Host — host Internet connection' },
  ...props.outbounds.map((item) => ({ value: item.id, label: item.enabled ? item.id : `${item.id} (disabled)` }))
])
const otpLink = computed(() => `otpauth://totp/Proximity:${encodeURIComponent(form.id || saved.value || 'OpenVPN')}?secret=${String(form.openvpn.otp_key).replace(/[\s=-]/g, '')}&issuer=Proximity`)

function defaults(kind) {
  const result = { id: '', enabled: true, autostart: true, statistics: true, port: null, outbound: '@router', anonymous: false, users: [] }
  if (kind === 'socks5') {
    result.socks5 = { listen: '*', udp: true, tls: false, certificate: '', key: '' }
  } else {
    result.openvpn = {
      server: window.location.hostname, public_port: null, listen: '*', protocol: 'udp', network: '', redirect_gateway: true, routes: [], dns: [],
      client_to_client: false, max_clients: 32, keepalive_interval: 10, keepalive_timeout: 60, renegotiation: 3600, mtu: 1500,
      ciphers: 'AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305', digest: 'SHA256', tls_version: '1.2', verbosity: 3, otp: false, otp_key: '',
      ca_certificate: '', ca_key: '', certificate: '', key: '', client_certificate: '', client_key: '', tls_crypt: ''
    }
  }
  return result
}

function apply(source) {
  const initial = defaults(props.kind)
  for (const key of Object.keys(form)) {
    delete form[key]
  }
  for (const key of Object.keys(initial)) {
    const value = source[key]
    form[key] = Array.isArray(initial[key]) ? JSON.parse(JSON.stringify(value || [])) : value && typeof value === 'object'
      ? { ...initial[key], ...JSON.parse(JSON.stringify(value)) } : value !== undefined ? value : initial[key]
  }
  if (form.openvpn) {
    form.openvpn.routes_text = (form.openvpn.routes || []).join('\n')
    form.openvpn.dns_text = (form.openvpn.dns || []).join(', ')
    form.openvpn.public_port = form.openvpn.public_port || null
  }
}

watch(() => props.open, async (opened) => {
  if (!opened) return
  tab.value = 'general'
  saved.value = props.inbound ? props.inbound.id : ''
  apply(props.inbound || {})
  if (!props.inbound && props.kind === 'openvpn') {
    await regenerate('all')
  }
}, { immediate: true })

function target(kind) {
  return kind === 'tls' ? form.socks5 : form.openvpn
}

async function regenerate(kind) {
  generating.value = kind
  try {
    const body = { kind }
    if (form.openvpn) {
      Object.assign(body, { ca_certificate: form.openvpn.ca_certificate, ca_key: form.openvpn.ca_key })
    }
    Object.assign(target(kind), await api.post('/api/inbounds/generate', body))
  } catch (error) {
    message.error(error.message)
  } finally {
    generating.value = ''
  }
}

function enableTLS(enabled) {
  if (enabled && !form.socks5.certificate && !form.socks5.key) {
    regenerate('tls')
  }
}

function randomPassword() {
  const values = new Uint32Array(16)
  crypto.getRandomValues(values)
  return Array.from(values, (value) => alphabet[value % alphabet.length]).join('')
}

function addUser() {
  form.users.push({ enabled: true, name: '', password: randomPassword() })
}

function userError(record, index) {
  const duplicate = form.users.some((other, position) => position < index && other.name === record.name)
  return { name: !namePattern.test(record.name) || duplicate, password: !passwordPattern.test(record.password) }
}

async function checkRoutes(rule, value) {
  for (const line of String(value || '').split('\n').map((item) => item.trim()).filter(Boolean)) {
    if (!networkPattern.test(line)) {
      throw new Error(`'${line}' is not an IPv4 network`)
    }
  }
}

async function checkTimeout(rule, value) {
  if (!Number.isInteger(value) || value <= form.openvpn.keepalive_interval || value > 3600) {
    throw new Error('Greater than the interval, up to 3600')
  }
}

async function copy(text, done) {
  try {
    await navigator.clipboard.writeText(text)
    message.success(done)
  } catch (error) {
    message.error('Clipboard is not available')
  }
}

function makeBody() {
  const body = {
    id: form.id, type: props.kind, enabled: form.enabled, autostart: form.autostart, statistics: form.statistics, port: form.port || 0,
    outbound: form.outbound, anonymous: form.anonymous, users: form.users.map((user) => ({ ...user, name: user.name.trim() }))
  }
  if (props.kind === 'socks5') {
    body.socks5 = { ...form.socks5 }
  } else {
    const { routes_text: routes, dns_text: servers, ...options } = form.openvpn
    body.openvpn = {
      ...options, public_port: options.public_port || 0, routes: String(routes || '').split('\n').map((item) => item.trim()).filter(Boolean),
      dns: String(servers || '').split(/[\s,]+/).filter(Boolean)
    }
  }
  return body
}

async function save(keep) {
  try {
    await formRef.value.validate()
  } catch (failure) {
    const first = failure.errorFields && failure.errorFields[0]
    if (first) {
      tab.value = ['socks5', 'openvpn'].includes(String(first.name[0])) ? 'type' : 'general'
    }
    return false
  }
  if (form.users.some((record, index) => userError(record, index).name || userError(record, index).password)) {
    tab.value = 'users'
    message.error('Check user names and passwords: names must be unique')
    return false
  }
  busy.value = true
  try {
    const result = saved.value
      ? await api.put(`/api/inbounds/${id(saved.value)}`, makeBody())
      : await api.post('/api/inbounds', makeBody())
    saved.value = result.id
    emit('saved')
    if (keep === true) {
      apply(result)
    } else {
      message.success('Inbound saved')
      emit('update:open', false)
    }
    return true
  } catch (error) {
    message.error(error.message)
    return false
  } finally {
    busy.value = false
  }
}

async function fetchExport() {
  try {
    const query = new URLSearchParams({ user: exported.user, embed: String(exported.embed), host: window.location.hostname })
    const data = await api.get(`/api/inbounds/${id(saved.value)}/config?${query}`, true)
    exported.config = data.config
    exported.file = data.file
  } catch (error) {
    exported.config = ''
    message.error(error.message)
  }
}

async function exportUser(name) {
  if (!(await save(true))) return
  Object.assign(exported, { user: name, embed: false, config: '', file: '', notice: 'The inbound was saved: the configuration matches the running server.' })
  await fetchExport()
  exportVisible.value = exported.config !== ''
}

function download() {
  const link = document.createElement('a')
  link.href = URL.createObjectURL(new Blob([exported.config], { type: 'text/plain' }))
  link.download = exported.file
  document.body.appendChild(link)
  link.click()
  link.remove()
  setTimeout(() => URL.revokeObjectURL(link.href), 1000)
}
</script>
