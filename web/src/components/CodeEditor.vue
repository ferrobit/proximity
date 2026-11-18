<template>
  <div :class="['code-editor', { 'rule-dark': dark, 'code-editor-disabled': disabled }]">
    <div v-if="$slots.toolbar" class="code-toolbar"><slot name="toolbar" /></div>
    <div class="code-body" :style="{ height: `${height}px` }">
      <div class="code-gutter" aria-hidden="true">
        <div :style="{ transform: `translateY(${-scroll.top}px)` }">
          <div v-for="number in highlighted.length" :key="number" class="code-line">{{ number }}</div>
        </div>
      </div>
      <div class="code-area">
        <pre class="code-highlight" aria-hidden="true" :style="{ transform: `translate(${-scroll.left}px, ${-scroll.top}px)` }"><template
          v-for="(line, index) in highlighted" :key="index"><span v-for="(part, position) in line" :key="position"
          :class="`rule-${part.kind}`">{{ part.text }}</span>{{ '\n' }}</template> </pre>
        <textarea class="code-input" :value="value" :disabled="disabled" :readonly="readonly" wrap="off" spellcheck="false"
          autocomplete="off" autocapitalize="off" :placeholder="placeholder" @input="changed($event.target.value)"
          @scroll="scrolled" @blur="formItem.onFieldBlur()" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, reactive } from 'vue'
import { Form, theme } from 'ant-design-vue'
import { highlighters } from './vpnSyntax'
import { useDarkTheme } from './ruleTheme'
import './rule-colors.css'

const props = defineProps({
  value: { type: String, default: '' },
  language: { type: String, default: 'openvpn' },
  disabled: Boolean,
  readonly: Boolean,
  placeholder: { type: String, default: '' },
  height: { type: Number, default: 240 }
})
const emit = defineEmits(['update:value', 'change'])

const formItem = Form.useInjectFormItemContext()
const { token } = theme.useToken()
const dark = useDarkTheme(token)
const scroll = reactive({ top: 0, left: 0 })
const highlighted = computed(() => (highlighters[props.language] || highlighters.openvpn)(props.value))

function changed(text) {
  emit('update:value', text)
  emit('change', text)
  formItem.onFieldChange()
}

function scrolled(event) {
  scroll.top = event.target.scrollTop
  scroll.left = event.target.scrollLeft
}
</script>

<style scoped>
.code-editor { border: 1px solid v-bind('token.colorBorder'); border-radius: v-bind('`${token.borderRadius}px`'); overflow: hidden;
  background: v-bind('token.colorBgContainer'); transition: border-color .2s, box-shadow .2s; }
.code-editor:hover { border-color: v-bind('token.colorPrimaryHover'); }
.code-editor:focus-within { border-color: v-bind('token.colorPrimary'); box-shadow: 0 0 0 2px v-bind('token.controlOutline'); }
.code-editor-disabled, .code-editor-disabled:hover { border-color: v-bind('token.colorBorder'); background: v-bind('token.colorBgContainerDisabled'); }
.code-toolbar { display: flex; align-items: center; gap: 2px; padding: 3px 6px; border-bottom: 1px solid v-bind('token.colorBorderSecondary');
  background: v-bind('token.colorFillQuaternary'); }
.code-body { display: flex; position: relative; }
.code-gutter { flex: none; width: 40px; overflow: hidden; padding: 6px 0; box-sizing: border-box; text-align: right;
  border-right: 1px solid v-bind('token.colorBorderSecondary'); background: v-bind('token.colorFillQuaternary');
  color: v-bind('token.colorTextQuaternary'); user-select: none; }
.code-line { padding-right: 8px; }
.code-area { position: relative; flex: 1; min-width: 0; overflow: hidden; }
.code-line, .code-highlight, .code-input { font-family: var(--font-mono); font-size: 12.5px; line-height: 20px; tab-size: 2; }
.code-highlight, .code-input { margin: 0; padding: 6px 10px; box-sizing: border-box; white-space: pre; letter-spacing: 0; font-variant-ligatures: none; }
.code-highlight { position: absolute; left: 0; top: 0; min-width: 100%; pointer-events: none; color: v-bind('token.colorText'); }
.code-input { position: absolute; inset: 0; width: 100%; height: 100%; resize: none; border: 0; outline: 0; background: transparent;
  color: transparent; caret-color: v-bind('token.colorText'); overflow: auto; }
.code-input::placeholder { color: v-bind('token.colorTextPlaceholder'); }
.code-input::selection { background: rgba(22, 119, 255, .25); color: transparent; }
.code-input:disabled { cursor: not-allowed; }
</style>
