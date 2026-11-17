<template>
  <div :class="['rule-editor', { 'rule-dark': dark, 'rule-editor-disabled': disabled }]">
    <div class="rule-toolbar">
      <a-tooltip title="Replace the parameters with the lines of a text file">
        <a-button size="small" type="text" :disabled="disabled" @click="chooser.click()">
          <template #icon><FolderOpenOutlined /></template>Load from file</a-button>
      </a-tooltip>
      <a-tooltip title="Download the parameters as a text file">
        <a-button size="small" type="text" :disabled="!value.trim()" @click="download">
          <template #icon><DownloadOutlined /></template>Save to file</a-button>
      </a-tooltip>
      <a-divider type="vertical" />
      <a-tooltip title="Sort lines alphabetically; a comment stays above the line that follows it">
        <a-button size="small" type="text" :disabled="disabled || counts.entries < 2" @click="sort">
          <template #icon><SortAscendingOutlined /></template>Sort A–Z</a-button>
      </a-tooltip>
      <span class="rule-spacer" />
      <span class="rule-counts">{{ counts.entries }} {{ counts.entries === 1 ? 'entry' : 'entries' }}<template v-if="counts.comments">
        · {{ counts.comments }} {{ counts.comments === 1 ? 'comment' : 'comments' }}</template></span>
      <input ref="chooser" type="file" accept=".txt,.list,.conf,.lst,text/plain" hidden @change="upload" />
    </div>
    <div class="rule-body" :style="{ height: `${height}px` }">
      <div class="rule-gutter" aria-hidden="true">
        <div :style="{ transform: `translateY(${-scroll.top}px)` }">
          <div v-for="number in lineCount" :key="number" class="rule-line">{{ number }}</div>
        </div>
      </div>
      <div class="rule-area">
        <pre class="rule-highlight" aria-hidden="true" :style="{ transform: `translate(${-scroll.left}px, ${-scroll.top}px)` }"><template
          v-for="(line, index) in highlighted" :key="index"><span v-for="(part, position) in line" :key="position"
          :class="`rule-${part.kind}`">{{ part.text }}</span>{{ '\n' }}</template> </pre>
        <textarea ref="input" class="rule-input" :value="value" :disabled="disabled" wrap="off" spellcheck="false"
          autocomplete="off" autocapitalize="off" :placeholder="placeholder" @input="changed($event.target.value)"
          @scroll="scrolled" @blur="formItem.onFieldBlur()" @keydown.tab.prevent="indent" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, reactive, ref } from 'vue'
import { Form, message, theme } from 'ant-design-vue'
import { DownloadOutlined, FolderOpenOutlined, SortAscendingOutlined } from '@ant-design/icons-vue'
import { countLines, highlightLine, sortLines } from './ruleSyntax'
import { useDarkTheme } from './ruleTheme'
import './rule-colors.css'

const props = defineProps({
  value: { type: String, default: '' },
  directive: { type: String, default: '' },
  disabled: Boolean,
  fileName: { type: String, default: 'route' },
  placeholder: { type: String, default: '' },
  height: { type: Number, default: 240 }
})
const emit = defineEmits(['update:value', 'change'])

const formItem = Form.useInjectFormItemContext()
const { token } = theme.useToken()
const dark = useDarkTheme(token)
const chooser = ref()
const input = ref()
const scroll = reactive({ top: 0, left: 0 })

const lines = computed(() => props.value.split('\n'))
const lineCount = computed(() => lines.value.length)
const highlighted = computed(() => lines.value.map((line) => highlightLine(line, props.directive)))
const counts = computed(() => countLines(props.value))

function changed(text) {
  emit('update:value', text)
  emit('change', text)
  formItem.onFieldChange()
}

function scrolled(event) {
  scroll.top = event.target.scrollTop
  scroll.left = event.target.scrollLeft
}

function indent(event) {
  const element = event.target
  const start = element.selectionStart
  const text = props.value.slice(0, start) + '  ' + props.value.slice(element.selectionEnd)
  changed(text)
  requestAnimationFrame(() => element.setSelectionRange(start + 2, start + 2))
}

async function upload(event) {
  const [file] = event.target.files
  event.target.value = ''
  if (!file) return
  if (file.size > 4 * 1024 * 1024) {
    message.error('The file is larger than 4 MB')
    return
  }
  const text = (await file.text()).replace(/^﻿/, '').replace(/\r\n?/g, '\n').replace(/\n+$/, '')
  changed(text)
  message.success(`Loaded ${countLines(text).entries} entries from ${file.name}`)
}

function download() {
  const name = (props.fileName || 'route').replace(/[^\p{L}\p{N}._-]+/gu, '-')
  const link = document.createElement('a')
  link.href = URL.createObjectURL(new Blob([props.value.replace(/\n*$/, '\n')], { type: 'text/plain' }))
  link.download = `${name}.txt`
  link.click()
  setTimeout(() => URL.revokeObjectURL(link.href), 1000)
}

function sort() {
  changed(sortLines(props.value))
}
</script>

<style scoped>
.rule-editor { border: 1px solid v-bind('token.colorBorder'); border-radius: v-bind('`${token.borderRadius}px`'); overflow: hidden;
  background: v-bind('token.colorBgContainer'); transition: border-color .2s, box-shadow .2s; }
.rule-editor:hover { border-color: v-bind('token.colorPrimaryHover'); }
.rule-editor:focus-within { border-color: v-bind('token.colorPrimary'); box-shadow: 0 0 0 2px v-bind('token.controlOutline'); }
.rule-editor-disabled, .rule-editor-disabled:hover { border-color: v-bind('token.colorBorder'); background: v-bind('token.colorBgContainerDisabled'); }
.rule-toolbar { display: flex; align-items: center; gap: 2px; padding: 3px 6px; border-bottom: 1px solid v-bind('token.colorBorderSecondary');
  background: v-bind('token.colorFillQuaternary'); }
.rule-spacer { flex: 1; }
.rule-counts { font-size: 12px; color: v-bind('token.colorTextTertiary'); padding-right: 4px; white-space: nowrap; }
.rule-body { display: flex; position: relative; }
.rule-gutter { flex: none; width: 40px; overflow: hidden; padding: 6px 0; box-sizing: border-box; text-align: right;
  border-right: 1px solid v-bind('token.colorBorderSecondary'); background: v-bind('token.colorFillQuaternary');
  color: v-bind('token.colorTextQuaternary'); user-select: none; }
.rule-line { padding-right: 8px; }
.rule-area { position: relative; flex: 1; min-width: 0; overflow: hidden; }
.rule-line, .rule-highlight, .rule-input { font-family: var(--font-mono); font-size: 12.5px; line-height: 20px; tab-size: 2; }
.rule-highlight, .rule-input { margin: 0; padding: 6px 10px; box-sizing: border-box; white-space: pre; letter-spacing: 0; font-variant-ligatures: none; }
.rule-highlight { position: absolute; left: 0; top: 0; min-width: 100%; pointer-events: none; color: v-bind('token.colorText'); }
.rule-input { position: absolute; inset: 0; width: 100%; height: 100%; resize: none; border: 0; outline: 0; background: transparent;
  color: transparent; caret-color: v-bind('token.colorText'); overflow: auto; }
.rule-input::placeholder { color: v-bind('token.colorTextPlaceholder'); }
.rule-input::selection { background: rgba(22, 119, 255, .25); color: transparent; }
.rule-input:disabled { cursor: not-allowed; }
</style>
