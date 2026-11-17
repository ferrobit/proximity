import { computed } from 'vue'

export function useDarkTheme(token) {
  return computed(() => {
    const color = token.value.colorBgContainer.replace('#', '')
    if (color.length < 6) return false
    const channels = [0, 2, 4].map((offset) => parseInt(color.slice(offset, offset + 2), 16))
    return channels.reduce((sum, channel) => sum + channel, 0) / 3 < 128
  })
}
