<template>
  <a-tooltip :title="status.message || undefined">
    <a-badge :status="stateColors[status.state] || 'default'" :text="stateLabels[status.state] || status.state" />
    <WarningOutlined v-if="status.message" style="margin-left: 6px; color: #faad14" />
  </a-tooltip>
</template>

<script setup>
import { WarningOutlined } from '@ant-design/icons-vue'
import { stateColors, stateLabels } from '../format'

defineProps({ status: { type: Object, required: true } })
</script>
