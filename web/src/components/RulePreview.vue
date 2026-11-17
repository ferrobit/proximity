<template>
  <div :class="['rule-preview', { 'rule-dark': dark }]">
    <div class="preview-toolbar">
      <span class="preview-counts">{{ lines.length }} router {{ lines.length === 1 ? 'rule' : 'rules' }}</span>
      <span class="preview-spacer" />
      <a-tooltip title="Copy the rules to the clipboard">
        <a-button size="small" type="text" @click="copy"><template #icon><CopyOutlined /></template>Copy</a-button>
      </a-tooltip>
    </div>
    <div class="preview-body">
      <div v-for="(line, index) in highlighted" :key="index" class="preview-row">
        <span class="preview-number">{{ index + 1 }}</span>
        <span class="preview-text"><span v-for="(part, position) in line" :key="position" :class="`rule-${part.kind}`">{{ part.text }}</span></span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { message, theme } from 'ant-design-vue'
import { CopyOutlined } from '@ant-design/icons-vue'
import { highlightRule } from './ruleSyntax'
import { useDarkTheme } from './ruleTheme'
import { copyText } from '../clipboard'
import './rule-colors.css'

const props = defineProps({ lines: { type: Array, default: () => [] } })

const { token } = theme.useToken()
const dark = useDarkTheme(token)
const highlighted = computed(() => props.lines.map(highlightRule))

async function copy() {
  try {
    await copyText(props.lines.join('\n') + '\n')
    message.success('Router rules copied')
  } catch (error) {
    message.error('Clipboard is not available')
  }
}
</script>

<style scoped>
.rule-preview { border: 1px solid v-bind('token.colorBorderSecondary'); border-radius: v-bind('`${token.borderRadius}px`'); overflow: hidden;
  background: v-bind('token.colorBgContainer'); }
.preview-toolbar { display: flex; align-items: center; padding: 3px 6px 3px 12px; border-bottom: 1px solid v-bind('token.colorBorderSecondary');
  background: v-bind('token.colorFillQuaternary'); }
.preview-spacer { flex: 1; }
.preview-counts { font-size: 12px; color: v-bind('token.colorTextTertiary'); }
.preview-body { max-height: 220px; overflow: auto; padding: 6px 0; }
.preview-row { display: flex; font-family: var(--font-mono); font-size: 12.5px; line-height: 20px; white-space: pre; }
.preview-row:hover { background: v-bind('token.colorFillQuaternary'); }
.preview-number { flex: none; width: 40px; padding-right: 8px; box-sizing: border-box; text-align: right; user-select: none;
  color: v-bind('token.colorTextQuaternary'); border-right: 1px solid v-bind('token.colorBorderSecondary'); }
.preview-text { padding: 0 10px; color: v-bind('token.colorText'); }
</style>
