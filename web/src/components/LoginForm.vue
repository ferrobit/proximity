<template>
  <a-form layout="vertical" :model="form" @finish="submit">
    <a-alert v-if="failure" type="error" :message="failure" show-icon style="margin-bottom: 16px" />
    <a-form-item label="User name" name="username" :rules="[{ required: true, message: 'Enter user name' }]">
      <a-input v-model:value="form.username" autocomplete="username" autofocus>
        <template #prefix><UserOutlined /></template>
      </a-input>
    </a-form-item>
    <a-form-item label="Password" name="password" :rules="[{ required: true, message: 'Enter password' }]">
      <a-input-password v-model:value="form.password" autocomplete="current-password">
        <template #prefix><LockOutlined /></template>
      </a-input-password>
    </a-form-item>
    <a-button type="primary" html-type="submit" block :loading="busy">
      <template #icon><LoginOutlined /></template>Sign in
    </a-button>
  </a-form>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { LockOutlined, LoginOutlined, UserOutlined } from '@ant-design/icons-vue'
import { api, loadSession } from '../api'

const emit = defineEmits(['done'])
const form = reactive({ username: '', password: '' })
const failure = ref('')
const busy = ref(false)

async function submit() {
  busy.value = true
  failure.value = ''
  try {
    await api.post('/api/login', form)
    await loadSession()
    form.password = ''
    emit('done')
  } catch (error) {
    failure.value = error.message
  } finally {
    busy.value = false
  }
}
</script>
