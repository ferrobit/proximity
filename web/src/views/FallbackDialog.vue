<template>
  <a-modal :open="open" :title="fallback ? `Edit fallback '${fallback.id}'` : 'Add fallback'" :confirm-loading="busy" destroy-on-close width="560px"
    ok-text="Save" @ok="save" @cancel="$emit('update:open', false)">
    <a-form ref="formRef" layout="vertical" :model="form">
      <a-row :gutter="16">
        <a-col :span="16">
          <a-form-item label="ID" name="id" :rules="[{ pattern: idPattern, message: 'Letters, digits, space, _ . - (up to 48)' }]"
            :extra="fallback ? 'Unique among outbounds and fallbacks' : 'Optional, allocated automatically when empty'">
            <a-input v-model:value="form.id" :placeholder="fallback ? fallback.id : 'Fallback N'" />
          </a-form-item>
        </a-col>
        <a-col :span="8">
          <a-form-item label="Enabled"><a-switch v-model:checked="form.enabled" /></a-form-item>
        </a-col>
      </a-row>
      <a-form-item label="Outbounds (in priority order)">
        <a-list size="small" bordered :data-source="form.outbounds" :locale="{ emptyText: 'Add at least one outbound' }">
          <template #renderItem="{ item, index }">
            <a-list-item>
              <span><b>{{ index + 1 }}.</b> {{ item }}</span>
              <template #actions>
                <a-button size="small" type="text" :disabled="index === 0" @click="shift(index, -1)"><template #icon><ArrowUpOutlined /></template></a-button>
                <a-button size="small" type="text" :disabled="index === form.outbounds.length - 1" @click="shift(index, 1)"><template #icon><ArrowDownOutlined /></template></a-button>
                <a-button size="small" type="text" danger @click="form.outbounds.splice(index, 1)"><template #icon><CloseOutlined /></template></a-button>
              </template>
            </a-list-item>
          </template>
        </a-list>
        <a-select style="width: 100%; margin-top: 8px" placeholder="Add outbound…" :value="undefined" :options="available" @select="form.outbounds.push($event)" />
      </a-form-item>
      <a-form-item extra="On: the router requests the URL through every outbound. Off: the first outbound in good state is used.">
        <a-checkbox v-model:checked="form.health_check">Enable health check</a-checkbox>
      </a-form-item>
      <a-row :gutter="16" v-if="form.health_check">
        <a-col :span="16">
          <a-form-item label="Health check URL" name="url" :rules="[{ type: 'url', message: 'Enter http(s) URL' }]">
            <a-input v-model:value="form.url" />
          </a-form-item>
        </a-col>
        <a-col :span="8">
          <a-form-item label="Interval, s" name="interval" :rules="[{ type: 'number', min: 10, max: 3600, message: '10…3600' }]">
            <a-input-number v-model:value="form.interval" style="width: 100%" />
          </a-form-item>
        </a-col>
      </a-row>
    </a-form>
  </a-modal>
</template>

<script setup>
import { computed, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ArrowDownOutlined, ArrowUpOutlined, CloseOutlined } from '@ant-design/icons-vue'
import { api, id } from '../api'

const props = defineProps({
  open: Boolean,
  fallback: { type: Object, default: null },
  targets: { type: Array, default: () => [] }
})
const emit = defineEmits(['update:open', 'saved'])

const idPattern = /^$|^[A-Za-z0-9]([A-Za-z0-9 _.-]{0,46}[A-Za-z0-9_.-])?$/
const busy = ref(false)
const formRef = ref()
const form = reactive({ id: '', enabled: true, outbounds: [], health_check: false, url: '', interval: 60 })

const available = computed(() => props.targets
  .filter((item) => item.kind === 'outbound' && !form.outbounds.includes(item.id))
  .map((item) => ({ value: item.id, label: item.enabled ? item.id : `${item.id} (disabled)` })))

watch(() => props.open, (opened) => {
  if (!opened) return
  const record = props.fallback
  Object.assign(form, record
    ? { ...record, outbounds: [...record.outbounds] }
    : { id: '', enabled: true, outbounds: [], health_check: false, url: 'https://www.gstatic.com/generate_204', interval: 60 })
}, { immediate: true })

function shift(index, delta) {
  const [item] = form.outbounds.splice(index, 1)
  form.outbounds.splice(index + delta, 0, item)
}

async function save() {
  try {
    await formRef.value.validate()
  } catch (failure) {
    return
  }
  if (!form.outbounds.length) {
    message.warning('Add at least one outbound')
    return
  }
  busy.value = true
  try {
    await (props.fallback ? api.put(`/api/fallbacks/${id(props.fallback.id)}`, form) : api.post('/api/fallbacks', form))
    emit('update:open', false)
    emit('saved')
  } catch (error) {
    message.error(error.message)
  } finally {
    busy.value = false
  }
}
</script>
