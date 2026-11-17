<template>
  <a-tooltip :title="status.message || undefined">
    <a-badge :status="toneBadges[stateTone(status.state)]" :text="stateLabels[status.state] || status.state" />
    <WarningOutlined v-if="status.message" style="margin-left: 6px; color: var(--tone-warn)" />
  </a-tooltip>
</template>

<script setup>
import { WarningOutlined } from '@ant-design/icons-vue'
import { stateLabels } from '../format'
import { stateTone, toneBadges } from '../states'

defineProps({ status: { type: Object, required: true } })
</script>
