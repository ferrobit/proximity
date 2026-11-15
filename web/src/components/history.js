import { onMounted, ref, watch } from 'vue'
import { api } from '../api'

const maxHistory = 3600

export const ranges = [{ value: 300, label: '5m' }, { value: 900, label: '15m' }, { value: 3600, label: '1h' }]

export function useHistory(source) {
  const samples = ref([])
  const step = ref(5)
  let loading = false
  async function load() {
    if (loading) return
    loading = true
    try {
      const last = samples.value[samples.value.length - 1]
      const result = await api.get(`/api/history${last ? `?since=${last.time}` : ''}`, true)
      const merged = samples.value.concat(result.samples)
      const cutoff = merged.length ? merged[merged.length - 1].time - maxHistory : 0
      step.value = result.step
      samples.value = merged.filter((sample) => sample.time > cutoff)
    } catch (error) {
      step.value = step.value || 5
    } finally {
      loading = false
    }
  }
  watch(source, load)
  onMounted(load)
  return { samples, step }
}

export function clock(time, seconds) {
  const date = new Date(time * 1000)
  const pad = (number) => String(number).padStart(2, '0')
  return `${pad(date.getHours())}:${pad(date.getMinutes())}${seconds ? `:${pad(date.getSeconds())}` : ''}`
}

export function timeTicks(end, range, spacing) {
  const result = []
  for (let tick = Math.ceil((end - range) / spacing) * spacing; tick <= end; tick += spacing) {
    result.push(tick)
  }
  return result
}
