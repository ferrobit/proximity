<template>
  <div>
    <div class="toolbar" style="margin-bottom: 8px">
      <a-badge :status="failure ? 'error' : 'processing'" :text="failure || `live · ${lines.length} lines`" />
      <span v-if="counts.error" class="log-count log-count-error">{{ counts.error }} {{ counts.error === 1 ? 'error' : 'errors' }}</span>
      <span v-if="counts.warning" class="log-count log-count-warning">{{ counts.warning }} {{ counts.warning === 1 ? 'warning' : 'warnings' }}</span>
      <span class="spacer" />
      <a-checkbox v-model:checked="follow">Follow new lines</a-checkbox>
      <a-button size="small" @click="copy"><template #icon><CopyOutlined /></template>Copy</a-button>
    </div>
    <div ref="view" :class="['mono', 'log-view', 'log-body', { 'log-dark': dark }]" @scroll="scrolled">
      <div v-if="!lines.length" class="log-empty">No messages yet</div>
      <div v-for="(line, index) in parsed" :key="index" :class="['log-row', `log-${line.severity}`]">
        <span v-if="line.date" class="log-stamp"><span class="log-date">{{ line.date }}</span> <span class="log-time">{{ line.time }}</span></span>
        <span class="log-message"><template v-for="(part, position) in line.parts" :key="position"><a v-if="part.kind === 'url'"
          :href="part.href" target="_blank" rel="noopener noreferrer" class="log-url">{{ part.text }}</a><span v-else
          :class="part.kind === 'level' ? ['log-level', `log-level-${part.level}`] : `log-${part.kind}`">{{ part.text }}</span></template></span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { message, theme } from 'ant-design-vue'
import { CopyOutlined } from '@ant-design/icons-vue'
import { api } from '../api'
import { copyText } from '../clipboard'
import { parseLogLine } from './logSyntax'
import { useDarkTheme } from './ruleTheme'

const props = defineProps({
  path: { type: String, required: true },
  interval: { type: Number, default: 1500 }
})
const lines = ref([])
const follow = ref(true)
const failure = ref('')
const view = ref()
let timer = null

const { token } = theme.useToken()
const dark = useDarkTheme(token)
const parsed = computed(() => lines.value.map(parseLogLine))
const counts = computed(() => parsed.value.reduce((result, line) => {
  result[line.severity] = (result[line.severity] || 0) + 1
  return result
}, {}))

async function load() {
  try {
    const data = await api.get(props.path, true)
    const changed = data.lines.length !== lines.value.length || data.lines[data.lines.length - 1] !== lines.value[lines.value.length - 1]
    failure.value = ''
    if (!changed) return
    lines.value = data.lines
    await nextTick()
    if (follow.value && view.value) {
      view.value.scrollTop = view.value.scrollHeight
    }
  } catch (error) {
    failure.value = error.message
  }
}

function scrolled() {
  const element = view.value
  follow.value = element.scrollHeight - element.scrollTop - element.clientHeight < 24
}

async function copy() {
  try {
    await copyText(lines.value.join('\n'))
    message.success('Log copied')
  } catch (error) {
    message.error('Clipboard is not available')
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, props.interval)
})
onUnmounted(() => clearInterval(timer))
</script>

<style scoped>
.log-body { padding: 6px 0; border: 1px solid v-bind('token.colorBorderSecondary'); border-radius: v-bind('`${token.borderRadius}px`');
  background: v-bind('token.colorFillQuaternary'); line-height: 1.6; word-break: normal; overflow-wrap: anywhere; }
.log-empty { padding: 4px 12px; color: v-bind('token.colorTextTertiary'); }
.log-row { display: flex; gap: 12px; padding: 0 12px 0 9px; border-left: 3px solid transparent; }
.log-row:hover { background: v-bind('token.colorFillTertiary'); }
.log-stamp { flex: none; white-space: nowrap; user-select: text; }
.log-date { color: v-bind('token.colorTextQuaternary'); }
.log-time { color: v-bind('token.colorTextTertiary'); font-weight: 600; }
.log-message { flex: 1; min-width: 0; color: v-bind('token.colorTextSecondary'); }
.log-info .log-message { color: #0e7f78; font-weight: 500; }
.log-success .log-message { color: #15803d; }
.log-debug .log-message { color: v-bind('token.colorTextQuaternary'); }
.log-warning { border-left-color: #f59e0b; background: rgba(245, 158, 11, .07); }
.log-warning .log-message { color: #b45309; }
.log-error { border-left-color: #ef4444; background: rgba(239, 68, 68, .08); }
.log-error .log-message { color: #dc2626; }
.log-quoted { color: #0f766e; }
.log-address { color: #1d4ed8; }
.log-url { color: #2563eb; text-decoration: underline; text-underline-offset: 2px; word-break: break-all; }
.log-url:hover { color: #1e40af; }
.log-level { display: inline-block; padding: 0 5px; border-radius: 4px; font-weight: 700; line-height: 1.4; }
.log-level-error { color: #fff; background: #dc2626; }
.log-level-warning { color: #1f1300; background: #fbbf24; }
.log-level-info { color: #fff; background: #0e9f97; }
.log-level-debug { color: #fff; background: #8c8c8c; }
.log-count { padding: 0 8px; border-radius: 10px; font-size: 12px; font-weight: 600; }
.log-count-error { color: #dc2626; background: rgba(239, 68, 68, .12); }
.log-count-warning { color: #b45309; background: rgba(245, 158, 11, .15); }
.log-dark .log-info .log-message { color: #2dd4bf; }
.log-dark .log-success .log-message { color: #4ade80; }
.log-dark .log-warning .log-message { color: #fbbf24; }
.log-dark .log-error .log-message { color: #f87171; }
.log-dark .log-quoted { color: #5eead4; }
.log-dark .log-address { color: #60a5fa; }
.log-dark .log-url { color: #93c5fd; }
.log-dark .log-url:hover { color: #bfdbfe; }
</style>
