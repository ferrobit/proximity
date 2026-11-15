<template>
  <div>
    <div class="toolbar" style="margin-bottom: 8px">
      <a-badge :status="failure ? 'error' : 'processing'" :text="failure || `live · ${lines.length} lines`" />
      <span class="spacer" />
      <a-checkbox v-model:checked="follow">Follow new lines</a-checkbox>
      <a-button size="small" @click="copy"><template #icon><CopyOutlined /></template>Copy</a-button>
    </div>
    <pre ref="view" class="mono log-view" @scroll="scrolled">{{ lines.join('\n') || 'No messages yet' }}</pre>
  </div>
</template>

<script setup>
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { CopyOutlined } from '@ant-design/icons-vue'
import { api } from '../api'

const props = defineProps({
  path: { type: String, required: true },
  interval: { type: Number, default: 1500 }
})
const lines = ref([])
const follow = ref(true)
const failure = ref('')
const view = ref()
let timer = null

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
    await navigator.clipboard.writeText(lines.value.join('\n'))
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
