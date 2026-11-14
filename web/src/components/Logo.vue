<template>
  <svg :width="size" :height="size" viewBox="0 0 64 64" aria-label="Proximity logo">
    <rect width="64" height="64" rx="14" fill="#0b1f2a" />
    <path d="M14 46a26 26 0 0 1 36-36" fill="none" stroke="#19c2b8" stroke-width="5" stroke-linecap="round" opacity=".45" />
    <path d="M21 46a19 19 0 0 1 26-26" fill="none" stroke="#19c2b8" stroke-width="5" stroke-linecap="round" opacity=".75" />
    <path d="M28 46a12 12 0 0 1 16-16" fill="none" stroke="#19c2b8" stroke-width="5" stroke-linecap="round" />
    <circle cx="46" cy="46" r="7" fill="#ffb347" />
  </svg>
</template>

<script setup>
defineProps({ size: { type: Number, default: 32 } })
</script>
