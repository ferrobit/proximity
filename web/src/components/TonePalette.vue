<template>
  <span hidden />
</template>

<script setup>
import { watchEffect } from 'vue'
import { theme } from 'ant-design-vue'
import { tonePalette } from '../states'

const { token } = theme.useToken()

watchEffect(() => {
  for (const [name, value] of Object.entries(tonePalette(token.value))) {
    document.documentElement.style.setProperty(name, value)
  }
})
</script>
