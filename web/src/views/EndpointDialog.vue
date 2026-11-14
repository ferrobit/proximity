<template>
  <a-modal :open="open" :title="title" width="720px" :confirm-loading="busy" ok-text="Save" destroy-on-close
    @ok="save" @cancel="$emit('update:open', false)">
    <a-form ref="formRef" layout="vertical" :model="form">
      <a-tabs v-model:active-key="tab">
        <a-tab-pane key="general" force-render>
          <template #tab><ProfileOutlined /> General</template>
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item label="ID" name="id" :rules="[{ pattern: idPattern, message: 'Letters, digits, space, _ . - (up to 48)' }]"
                extra="Unique, allocated automatically when empty">
                <a-input v-model:value="form.id" :disabled="!!endpoint" :placeholder="`${shortLabels[kind]} N`" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="Name" name="name" :rules="[{ max: 96, message: 'Up to 96 characters' }]">
                <a-input v-model:value="form.name" placeholder="Free text description" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="Group" name="group" :rules="[{ max: 48, message: 'Up to 48 characters' }]">
                <a-auto-complete v-model:value="form.group" :options="groups.map((value) => ({ value }))" placeholder="New or existing group" />
              </a-form-item>
            </a-col>
            <a-col :span="6"><a-form-item label="Enabled"><a-switch v-model:checked="form.enabled" /></a-form-item></a-col>
            <a-col :span="6"><a-form-item label="Collect statistics"><a-switch v-model:checked="form.statistics" /></a-form-item></a-col>
          </a-row>
        </a-tab-pane>
        <a-tab-pane key="type" force-render>
          <template #tab><ControlOutlined /> {{ typeLabels[kind] }}</template>
          <template v-if="kind === 'host'">
            <a-form-item label="Outgoing connection">
              <a-radio-group v-model:value="form.host.mode">
                <a-radio value="default">Default host routing</a-radio>
                <a-radio value="interface">Specific network interface</a-radio>
              </a-radio-group>
            </a-form-item>
            <a-row :gutter="16" v-if="form.host.mode === 'interface'">
              <a-col :span="14">
                <a-form-item label="Interface" :name="['host', 'interface']" :rules="[{ required: true, message: 'Select interface' }]">
                  <a-select v-model:value="form.host.interface" show-search :options="interfaceOptions" placeholder="Select interface" />
                </a-form-item>
              </a-col>
              <a-col :span="10">
                <a-form-item label="Gateway (optional)" :name="['host', 'gateway']" :rules="[{ pattern: ipPattern, message: 'Enter IP address' }]"
                  extra="Needed when the interface has no default route">
                  <a-input v-model:value="form.host.gateway" placeholder="192.168.1.1" />
                </a-form-item>
              </a-col>
            </a-row>
          </template>
          <template v-else-if="kind === 'socks5'">
            <a-row :gutter="16">
              <a-col :span="16"><a-form-item label="Server" :name="['socks5', 'server']" :rules="serverRules"><a-input v-model:value="form.socks5.server" placeholder="IP address or hostname" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Port" :name="['socks5', 'port']" :rules="portRules"><a-input-number v-model:value="form.socks5.port" style="width: 100%" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item label="User name (optional)"><a-input v-model:value="form.socks5.username" autocomplete="off" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item label="Password (optional)"><a-input-password v-model:value="form.socks5.password" autocomplete="new-password" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="UDP relay"><a-switch v-model:checked="form.socks5.udp" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="TLS to server"><a-switch v-model:checked="form.socks5.tls" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Skip certificate check"><a-switch v-model:checked="form.socks5.skip_cert_verify" :disabled="!form.socks5.tls" /></a-form-item></a-col>
            </a-row>
          </template>
          <template v-else-if="kind === 'http'">
            <a-row :gutter="16">
              <a-col :span="16"><a-form-item label="HTTP proxy server" :name="['http', 'server']" :rules="serverRules"><a-input v-model:value="form.http.server" placeholder="IP address or hostname" /></a-form-item></a-col>
              <a-col :span="8"><a-form-item label="Port" :name="['http', 'port']" :rules="portRules"><a-input-number v-model:value="form.http.port" style="width: 100%" /></a-form-item></a-col>
              <a-col :span="24"><a-form-item><a-checkbox v-model:checked="form.http.single">Use this proxy server for all protocols</a-checkbox></a-form-item></a-col>
              <template v-if="!form.http.single">
                <a-col :span="16"><a-form-item label="HTTPS proxy server" :name="['http', 'https_server']" :rules="optionalServerRules"><a-input v-model:value="form.http.https_server" placeholder="Empty: use HTTP proxy" /></a-form-item></a-col>
                <a-col :span="8"><a-form-item label="Port" :name="['http', 'https_port']" :rules="form.http.https_server ? portRules : []"><a-input-number v-model:value="form.http.https_port" style="width: 100%" /></a-form-item></a-col>
                <a-col :span="16"><a-form-item label="FTP proxy server" :name="['http', 'ftp_server']" :rules="optionalServerRules"><a-input v-model:value="form.http.ftp_server" placeholder="Empty: use HTTP proxy" /></a-form-item></a-col>
                <a-col :span="8"><a-form-item label="Port" :name="['http', 'ftp_port']" :rules="form.http.ftp_server ? portRules : []"><a-input-number v-model:value="form.http.ftp_port" style="width: 100%" /></a-form-item></a-col>
              </template>
              <a-col :span="12"><a-form-item label="User name (optional)"><a-input v-model:value="form.http.username" autocomplete="off" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item label="Password (optional)"><a-input-password v-model:value="form.http.password" autocomplete="new-password" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item label="TLS to proxy (HTTPS proxy)"><a-switch v-model:checked="form.http.tls" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item label="Skip certificate check"><a-switch v-model:checked="form.http.skip_cert_verify" :disabled="!form.http.tls" /></a-form-item></a-col>
            </a-row>
          </template>
          <template v-else-if="kind === 'openvpn'">
            <a-form-item label="VPN configuration file" :name="['openvpn', 'config']" :rules="[{ required: true, message: 'Upload or paste .ovpn configuration' }]">
              <a-upload :before-upload="readText('openvpn')" :show-upload-list="false" accept=".ovpn,.conf,.txt">
                <a-button size="small" style="margin-bottom: 8px"><template #icon><UploadOutlined /></template>Upload .ovpn file</a-button>
              </a-upload>
              <a-textarea v-model:value="form.openvpn.config" class="mono" :rows="10" placeholder="client&#10;dev tun&#10;remote vpn.example.com 1194 udp&#10;…" spellcheck="false" />
            </a-form-item>
            <a-row :gutter="16">
              <a-col :span="12"><a-form-item label="User name (optional)"><a-input v-model:value="form.openvpn.username" autocomplete="off" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item label="User password (optional)"><a-input-password v-model:value="form.openvpn.password" autocomplete="new-password" /></a-form-item></a-col>
              <a-col :span="12">
                <a-form-item label="OTP key (optional)" :name="['openvpn', 'otp_key']" :rules="[{ pattern: /^[A-Za-z2-7 =-]*$/, message: 'Base32 key expected' }]"
                  extra="Base32 secret used to answer the challenge (TOTP)">
                  <a-input-password v-model:value="form.openvpn.otp_key" autocomplete="off" />
                </a-form-item>
              </a-col>
              <a-col :span="12">
                <a-form-item label="OTP code usage">
                  <a-select v-model:value="form.openvpn.otp_mode" :options="otpModes" />
                </a-form-item>
              </a-col>
              <a-col :span="12"><a-form-item label="Private key password (optional)"><a-input-password v-model:value="form.openvpn.key_password" autocomplete="new-password" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item label="Use DNS servers pushed by VPN" extra="For domains routed to this endpoint"><a-switch v-model:checked="form.openvpn.use_dns" /></a-form-item></a-col>
            </a-row>
          </template>
          <template v-else>
            <a-form-item label="AmneziaWG configuration" :name="['amneziawg', 'config']" :rules="[{ required: true, message: 'Paste configuration or import it' }]">
              <a-space style="margin-bottom: 8px" wrap>
                <a-upload :before-upload="readText('amneziawg')" :show-upload-list="false" accept=".conf,.txt">
                  <a-button size="small"><template #icon><UploadOutlined /></template>Upload .conf file</a-button>
                </a-upload>
                <a-upload :before-upload="readCode" :show-upload-list="false" accept="image/*">
                  <a-button size="small"><template #icon><QrcodeOutlined /></template>Scan QR code from image</a-button>
                </a-upload>
                <a-button size="small" @click="importKey"><template #icon><ImportOutlined /></template>Convert vpn:// key</a-button>
              </a-space>
              <a-textarea v-model:value="form.amneziawg.config" class="mono" :rows="12" placeholder="[Interface]&#10;PrivateKey = …&#10;Address = 10.8.1.2/32&#10;Jc = 4&#10;…&#10;[Peer]&#10;PublicKey = …&#10;Endpoint = host:port&#10;AllowedIPs = 0.0.0.0/0" spellcheck="false" />
            </a-form-item>
            <a-form-item label="Use DNS servers from configuration" extra="For domains routed to this endpoint"><a-switch v-model:checked="form.amneziawg.use_dns" /></a-form-item>
          </template>
        </a-tab-pane>
        <a-tab-pane v-if="tunnel" key="monitoring" force-render>
          <template #tab><HeartOutlined /> Monitoring</template>
          <a-form-item label="Enable monitoring"><a-switch v-model:checked="form.monitoring.enabled" /></a-form-item>
          <a-alert type="info" show-icon style="margin-bottom: 16px" :message="kind === 'openvpn'
            ? 'Pings the default gateway obtained from the VPN through the tunnel.'
            : 'Opens a TCP connection to the check target through the tunnel.'" />
          <a-row :gutter="16">
            <a-col :span="24" v-if="kind === 'amneziawg'">
              <a-form-item label="Check target (host:port)" :name="['monitoring', 'target']" :rules="[{ pattern: /^[^\s:]+:\d{1,5}$|^\[[0-9a-fA-F:]+\]:\d{1,5}$/, message: 'host:port expected' }]">
                <a-input v-model:value="form.monitoring.target" placeholder="1.1.1.1:443" />
              </a-form-item>
            </a-col>
            <a-col :span="12"><a-form-item label="Check every, seconds" :name="['monitoring', 'interval']" :rules="range(5, 3600)"><a-input-number v-model:value="form.monitoring.interval" style="width: 100%" /></a-form-item></a-col>
            <a-col :span="12"><a-form-item label="Wait for response, seconds" :name="['monitoring', 'timeout']" :rules="range(1, 60)"><a-input-number v-model:value="form.monitoring.timeout" style="width: 100%" /></a-form-item></a-col>
            <a-col :span="12"><a-form-item label="If check fails, repeat it, times" :name="['monitoring', 'retries']" :rules="range(1, 20)"><a-input-number v-model:value="form.monitoring.retries" style="width: 100%" /></a-form-item></a-col>
            <a-col :span="12"><a-form-item label="If repeat fails, reconnect after, seconds" :name="['monitoring', 'reconnect_delay']" :rules="range(1, 3600)"><a-input-number v-model:value="form.monitoring.reconnect_delay" style="width: 100%" /></a-form-item></a-col>
            <a-col :span="12"><a-form-item label="Reconnect attempts" :name="['monitoring', 'reconnect_attempts']" :rules="range(0, 100)"
              extra="When all attempts fail the connection is taken down and marked as failed"><a-input-number v-model:value="form.monitoring.reconnect_attempts" style="width: 100%" /></a-form-item></a-col>
          </a-row>
        </a-tab-pane>
      </a-tabs>
    </a-form>
  </a-modal>
</template>

<script setup>
import { computed, reactive, ref, watch } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { ControlOutlined, HeartOutlined, ImportOutlined, ProfileOutlined, QrcodeOutlined, UploadOutlined } from '@ant-design/icons-vue'
import jsQR from 'jsqr'
import { api, id } from '../api'
import { typeLabels } from '../format'

const props = defineProps({
  open: Boolean,
  endpoint: { type: Object, default: null },
  kind: { type: String, default: 'host' },
  groups: { type: Array, default: () => [] }
})
const emit = defineEmits(['update:open', 'saved'])

const shortLabels = { host: 'HOST', socks5: 'SOCKS5', http: 'HTTP', openvpn: 'OpenVPN', amneziawg: 'AmneziaWG' }
const idPattern = /^$|^[A-Za-z0-9]([A-Za-z0-9 _.-]{0,46}[A-Za-z0-9_.-])?$/
const ipPattern = /^$|^(\d{1,3}\.){3}\d{1,3}$|^[0-9a-fA-F:]+$/
const hostPattern = /^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$|^[0-9a-fA-F:]+$/
const serverRules = [{ required: true, pattern: hostPattern, message: 'Enter IP address or hostname' }]
const optionalServerRules = [{ pattern: new RegExp(`^$|${hostPattern.source}`), message: 'Enter IP address or hostname' }]
const portRules = [{ required: true, type: 'number', min: 1, max: 65535, message: '1…65535' }]
const otpModes = [
  { value: 'challenge', label: 'Answer the CHALLENGE prompt' },
  { value: 'append', label: 'Append code to the password' }
]
const range = (min, max) => [{ required: true, type: 'number', min, max, message: `${min}…${max}` }]

const formRef = ref()
const tab = ref('general')
const busy = ref(false)
const interfaces = ref([])
const form = reactive({})

const tunnel = computed(() => props.kind === 'openvpn' || props.kind === 'amneziawg')
const title = computed(() => props.endpoint ? `Edit endpoint '${props.endpoint.id}'` : `Add ${typeLabels[props.kind]} endpoint`)
const interfaceOptions = computed(() => interfaces.value.map((item) => ({
  value: item.name,
  label: `${item.name} (${item.state}${item.addresses.length ? `, ${item.addresses.join(', ')}` : ''})`
})))

function defaults(kind) {
  return {
    id: '', name: '', group: '', type: kind, enabled: true, statistics: true,
    host: { mode: 'default', interface: undefined, gateway: '' },
    socks5: { server: '', port: 1080, username: '', password: '', tls: false, skip_cert_verify: false, udp: true },
    http: { server: '', port: 3128, single: true, https_server: '', https_port: 3128, ftp_server: '', ftp_port: 3128, username: '', password: '', tls: false, skip_cert_verify: false },
    openvpn: { config: '', username: '', password: '', otp_key: '', otp_mode: 'challenge', key_password: '', use_dns: true },
    amneziawg: { config: '', use_dns: true },
    monitoring: { enabled: true, interval: 30, timeout: 5, retries: 3, reconnect_delay: 10, reconnect_attempts: 3, target: '1.1.1.1:443' }
  }
}

watch(() => props.open, async (opened) => {
  if (!opened) return
  tab.value = 'general'
  const initial = defaults(props.kind)
  const source = props.endpoint ? JSON.parse(JSON.stringify(props.endpoint)) : {}
  for (const key of Object.keys(initial)) {
    const value = source[key]
    form[key] = value && typeof value === 'object' ? { ...initial[key], ...value } : value !== undefined ? value : initial[key]
  }
  if (props.kind === 'host') {
    try {
      interfaces.value = await api.get('/api/interfaces', true)
    } catch (error) {
      interfaces.value = []
    }
  }
})

function readText(target) {
  return (file) => {
    const reader = new FileReader()
    reader.onload = () => { form[target].config = String(reader.result) }
    reader.readAsText(file)
    return false
  }
}

async function convert(text) {
  try {
    form.amneziawg.config = (await api.post('/api/import/amneziawg', { text })).config
    message.success('Configuration imported')
  } catch (error) {
    message.error(error.message)
  }
}

function readCode(file) {
  const reader = new FileReader()
  reader.onload = () => {
    const image = new Image()
    image.onload = () => {
      const canvas = document.createElement('canvas')
      canvas.width = image.naturalWidth
      canvas.height = image.naturalHeight
      const context = canvas.getContext('2d')
      context.drawImage(image, 0, 0)
      const pixels = context.getImageData(0, 0, canvas.width, canvas.height)
      const code = jsQR(pixels.data, pixels.width, pixels.height)
      if (code && code.data) {
        convert(code.data)
      } else {
        message.error('QR code is not found in the image')
      }
    }
    image.onerror = () => message.error('Cannot read the image')
    image.src = String(reader.result)
  }
  reader.readAsDataURL(file)
  return false
}

function importKey() {
  const text = form.amneziawg.config.trim()
  if (!text.startsWith('vpn://')) {
    Modal.info({ title: 'Convert vpn:// key', content: 'Paste the Amnezia key (vpn://…) into the configuration field and press this button again.' })
    return
  }
  convert(text)
}

async function save() {
  try {
    await formRef.value.validate()
  } catch (failure) {
    const first = failure.errorFields && failure.errorFields[0]
    if (first) {
      const group = String(first.name[0])
      tab.value = group === 'monitoring' ? 'monitoring' : ['id', 'name', 'group'].includes(group) ? 'general' : 'type'
    }
    return
  }
  const body = { id: form.id, name: form.name, group: form.group, type: props.kind, enabled: form.enabled, statistics: form.statistics }
  body[props.kind] = { ...form[props.kind] }
  if (props.kind === 'host' && body.host.mode !== 'interface') {
    body.host = { mode: 'default', interface: '', gateway: '' }
  }
  if (tunnel.value) {
    body.monitoring = { ...form.monitoring }
  }
  busy.value = true
  try {
    if (props.endpoint) {
      await api.put(`/api/endpoints/${id(props.endpoint.id)}`, body)
    } else {
      await api.post('/api/endpoints', body)
    }
    message.success('Endpoint saved')
    emit('update:open', false)
    emit('saved')
  } catch (error) {
    message.error(error.message)
  } finally {
    busy.value = false
  }
}
</script>
