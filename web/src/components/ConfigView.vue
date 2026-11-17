<template>
  <div :class="['config-view', { 'rule-dark': dark }]">
    <div class="config-toolbar">
      <span class="config-counts">{{ lines.length }} {{ lines.length === 1 ? 'line' : 'lines' }}<template v-if="hide && hidden.count"> · {{ hidden.count }} hidden</template></span>
      <span class="config-spacer" />
      <a-radio-group v-model:value="format" size="small" class="config-format">
        <a-radio-button value="json">JSON</a-radio-button>
        <a-radio-button value="yaml">YAML</a-radio-button>
      </a-radio-group>
      <a-tooltip :title="`Passwords and private keys are replaced with ${hiddenValue} in the view, in Copy and in Save`">
        <label class="config-switch"><a-switch v-model:checked="hide" size="small" /><span>Hide passwords</span></label>
      </a-tooltip>
      <a-tooltip title="Copy the configuration to the clipboard">
        <a-button size="small" type="text" :disabled="!shown" @click="copy"><template #icon><CopyOutlined /></template>Copy</a-button>
      </a-tooltip>
      <a-tooltip :title="`Save as ${savedName}`">
        <a-button size="small" type="text" :disabled="!shown" @click="save"><template #icon><DownloadOutlined /></template>Save</a-button>
      </a-tooltip>
    </div>
    <div class="config-body">
      <div v-for="(line, index) in lines" :key="index" class="config-row">
        <span class="config-number">{{ index + 1 }}</span>
        <span class="config-text"><span v-for="(part, position) in line" :key="position" :class="`rule-${part.kind}`">{{ part.text }}</span></span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { message, theme } from 'ant-design-vue'
import { CopyOutlined, DownloadOutlined } from '@ant-design/icons-vue'
import { convertYaml, hiddenValue, hideSecrets, highlightJson, highlightYaml } from './configSyntax'
import { useDarkTheme } from './ruleTheme'
import { copyText } from '../clipboard'
import './rule-colors.css'

const props = defineProps({ text: { type: String, default: '' }, fileName: { type: String, default: 'config' } })

const { token } = theme.useToken()
const dark = useDarkTheme(token)
const hideKey = 'proximity-config-hide-passwords'
const formatKey = 'proximity-config-format'
const hide = ref(readStored(hideKey) !== 'off')
const format = ref(readStored(formatKey) === 'yaml' ? 'yaml' : 'json')
const hidden = computed(() => hideSecrets(props.text))
const json = computed(() => hide.value ? hidden.value.text : props.text)
const converted = computed(() => {
  try {
    return { text: json.value ? convertYaml(json.value) : '', error: '' }
  } catch (error) {
    return { text: '', error: error.message }
  }
})
const shown = computed(() => format.value === 'yaml' && !converted.value.error ? converted.value.text : json.value)
const lines = computed(() => shown.value ? (format.value === 'yaml' && !converted.value.error ? highlightYaml : highlightJson)(shown.value) : [])
const savedName = computed(() => `${props.fileName}.${format.value === 'yaml' && !converted.value.error ? 'yaml' : 'json'}`)

watch(hide, (value) => writeStored(hideKey, value ? 'on' : 'off'))
watch(format, (value) => writeStored(formatKey, value))
watch(() => converted.value.error, (error) => {
  if (error && format.value === 'yaml') message.error(`Cannot convert the configuration to YAML: ${error}`)
})

function readStored(key) {
  try {
    return localStorage.getItem(key)
  } catch (error) {
    return null
  }
}

function writeStored(key, value) {
  try {
    localStorage.setItem(key, value)
  } catch (error) {
    return
  }
}

async function copy() {
  try {
    await copyText(shown.value)
    message.success(hide.value && hidden.value.count ? 'Configuration copied without passwords' : 'Configuration copied')
  } catch (error) {
    message.error('Clipboard is not available')
  }
}

function save() {
  const link = document.createElement('a')
  const type = savedName.value.endsWith('.yaml') ? 'application/yaml' : 'application/json'
  link.href = URL.createObjectURL(new Blob([shown.value], { type }))
  link.download = savedName.value
  link.click()
  URL.revokeObjectURL(link.href)
}
</script>

<style scoped>
.config-view { border: 1px solid v-bind('token.colorBorderSecondary'); border-radius: v-bind('`${token.borderRadius}px`'); overflow: hidden;
  background: v-bind('token.colorBgContainer'); }
.config-toolbar { display: flex; align-items: center; gap: 2px; padding: 3px 6px 3px 12px; border-bottom: 1px solid v-bind('token.colorBorderSecondary');
  background: v-bind('token.colorFillQuaternary'); }
.config-spacer { flex: 1; }
.config-format { margin-right: 12px; }
.config-switch { display: inline-flex; align-items: center; gap: 6px; margin-right: 8px; font-size: 13px; cursor: pointer;
  color: v-bind('token.colorTextSecondary'); }
.config-counts { font-size: 12px; color: v-bind('token.colorTextTertiary'); }
.config-body { max-height: 65vh; overflow: auto; padding: 6px 0; }
.config-row { display: flex; font-family: var(--font-mono); font-size: 12.5px; line-height: 20px; white-space: pre; }
.config-row:hover { background: v-bind('token.colorFillQuaternary'); }
.config-number { flex: none; width: 48px; padding-right: 8px; box-sizing: border-box; text-align: right; user-select: none;
  color: v-bind('token.colorTextQuaternary'); border-right: 1px solid v-bind('token.colorBorderSecondary'); }
.config-text { padding: 0 10px; color: v-bind('token.colorText'); }
</style>
