<template>
  <a-modal :open="open" :title="title" width="680px" :confirm-loading="busy" destroy-on-close
    :ok-button-props="{ disabled: readOnly }" ok-text="Save" @ok="save" @cancel="$emit('update:open', false)">
    <a-form ref="formRef" layout="vertical" :model="form">
      <a-row :gutter="16">
        <a-col :span="16"><a-form-item label="ID" name="id" :rules="[{ max: 64, message: 'Up to 64 characters' }]" extra="Unique among routes, allocated automatically when empty">
          <a-input v-model:value="form.id" :disabled="readOnly" :placeholder="route ? route.id : 'Route N'" /></a-form-item></a-col>
        <a-col :span="8"><a-form-item label="Enabled"><a-switch v-model:checked="form.enabled" :disabled="readOnly" /></a-form-item></a-col>
        <a-col :span="12"><a-form-item label="Directive" name="directive" :rules="[{ required: true }]">
          <a-select v-model:value="form.directive" :disabled="readOnly" show-search :options="directives.map((value) => ({ value }))" @change="preview" /></a-form-item></a-col>
        <a-col :span="12"><a-form-item label="Target" name="target" :rules="[{ required: true, message: 'Select target' }]">
          <a-select v-model:value="form.target" :disabled="readOnly" show-search :options="targetOptions(targets)" @change="preview" /></a-form-item></a-col>
      </a-row>
      <a-form-item label="Rule parameters (one per line, lines starting with # are comments)" name="text" :rules="[{ required: true, message: 'Enter at least one parameter' }]" :extra="hints[form.directive]">
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
</template>

<script setup>
import { computed, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { api, id } from '../api'
import { targetOptions } from '../format'

const props = defineProps({
  open: Boolean,
  route: { type: Object, default: null },
  targets: { type: Array, default: () => [] },
  directives: { type: Array, default: () => [] }
})
const emit = defineEmits(['update:open', 'saved'])

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
const formRef = ref()
const busy = ref(false)
const form = reactive({ id: '', directive: 'DOMAIN-SUFFIX', target: undefined, enabled: true, text: '' })
const previewLines = ref([])
const previewError = ref('')
let previewTimer = null

const readOnly = computed(() => !!(props.route && props.route.provided))
const title = computed(() => !props.route ? 'Add route' : readOnly.value ? 'Provided route (read-only)' : `Edit route '${props.route.id}'`)

watch(() => props.open, (opened) => {
  if (!opened) return
  const record = props.route
  Object.assign(form, record
    ? { id: record.id, directive: record.directive, target: record.target, enabled: record.enabled, text: record.params.join('\n') }
    : { id: '', directive: 'DOMAIN-SUFFIX', target: undefined, enabled: true, text: '' })
  previewLines.value = []
  previewError.value = ''
  if (record) preview()
}, { immediate: true })

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
      previewLines.value = (await api.post('/api/routes/preview', { ...body(), target: form.target || 'TARGET' })).lines
      previewError.value = ''
    } catch (error) {
      previewError.value = error.message
    }
  }, 350)
}

async function save() {
  try {
    await formRef.value.validate()
  } catch (failure) {
    return
  }
  busy.value = true
  try {
    await (props.route ? api.put(`/api/routes/${id(props.route.key)}`, body()) : api.post('/api/routes', body()))
    emit('update:open', false)
    emit('saved')
  } catch (error) {
    message.error(error.message)
  } finally {
    busy.value = false
  }
}
</script>
