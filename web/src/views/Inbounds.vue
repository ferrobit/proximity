<template>
  <div>
    <h2 class="pane-title"><ImportOutlined /> Inbounds</h2>
    <a-card>
      <a-result title="Inbounds will be implemented in a future version"
        sub-title="An inbound is an incoming connection point: a port or protocol on which Proximity accepts traffic from clients. Today all clients use the built-in mixed proxy port and, in host network mode, transparent capture.">
        <template #icon><ImportOutlined class="inbounds-icon" /></template>
      </a-result>
    </a-card>
  </div>
</template>

<script setup>
import { ImportOutlined } from '@ant-design/icons-vue'
</script>

<style scoped>
.inbounds-icon { font-size: 72px; opacity: .35; }
</style>
