<template>
  <a-config-provider :theme="theme">
    <a-layout v-if="session.loaded" class="shell">
      <a-layout-header class="header" :style="{ background: dark ? '#0b1f2a' : '#0b1f2a' }">
        <div class="brand">
          <Logo :size="34" />
          <span class="brand-name">Proximity</span>
        </div>
        <div class="header-stats" v-if="overview">
          <a-tooltip title="Outbounds in good state / enabled">
            <span class="stat"><ExportOutlined /> {{ overview.counts.outbounds_up }}/{{ overview.counts.outbounds_enabled }}</span>
          </a-tooltip>
          <a-tooltip title="Upload rate">
            <span class="stat"><ArrowUpOutlined /> {{ rate(overview.total.upload_rate) }}</span>
          </a-tooltip>
          <a-tooltip title="Download rate">
            <span class="stat"><ArrowDownOutlined /> {{ rate(overview.total.download_rate) }}</span>
          </a-tooltip>
          <a-tooltip title="Active connections">
            <span class="stat"><SwapOutlined /> {{ overview.total.connections }}</span>
          </a-tooltip>
          <a-tooltip v-if="overview.capture" title="All outgoing connections of the host are routed through Proximity">
            <span class="stat"><GatewayOutlined /> transparent</span>
          </a-tooltip>
          <a-tooltip :title="overview.failure || overview.router.message || 'Router service state'">
            <span class="stat">
              <a-badge :status="routerBadge" />
              <span>router {{ overview.router.running ? 'running' : 'stopped' }}</span>
            </span>
          </a-tooltip>
        </div>
        <div class="header-user">
          <a-tooltip :title="dark ? 'Light theme' : 'Dark theme'">
            <a-button type="text" class="header-button" @click="toggleTheme">
              <template #icon><BulbOutlined /></template>
            </a-button>
          </a-tooltip>
          <a-dropdown v-if="session.level > 0">
            <a-button type="text" class="header-button">
              <UserOutlined /> {{ session.user }}
              <a-tag :color="roleColors[session.role]" style="margin-left: 8px">{{ session.role }}</a-tag>
            </a-button>
            <template #overlay>
              <a-menu>
                <a-menu-item key="password" @click="passwordOpen = true"><KeyOutlined /> Change password</a-menu-item>
                <a-menu-item key="logout" @click="logout"><LogoutOutlined /> Sign out</a-menu-item>
              </a-menu>
            </template>
          </a-dropdown>
          <a-button v-else type="primary" @click="loginOpen = true">
            <template #icon><LoginOutlined /></template>Sign in
          </a-button>
        </div>
      </a-layout-header>
      <a-layout>
        <a-layout-sider :theme="dark ? 'dark' : 'light'" :width="210" collapsible v-model:collapsed="collapsed">
          <a-menu mode="inline" :theme="dark ? 'dark' : 'light'" :selected-keys="[section]" @click="navigate($event.key)">
            <a-menu-item v-for="item in visibleSections" :key="item.key">
              <component :is="item.icon" /><span>{{ item.title }}</span>
            </a-menu-item>
          </a-menu>
        </a-layout-sider>
        <a-layout>
          <a-layout-content class="content">
            <a-result v-if="locked" status="403" title="Sign in required" sub-title="This instance does not allow anonymous access.">
              <template #extra>
                <div style="max-width: 320px; margin: 0 auto; text-align: left"><LoginForm @done="afterLogin" /></div>
              </template>
            </a-result>
            <component v-else :is="currentView" :overview="overview" @refresh="refreshOverview" />
          </a-layout-content>
          <a-layout-footer class="footer">
            <span>Proximity {{ session.version }} · © {{ year }} Proximity project</span>
            <span>page generated in {{ metrics.total.toFixed(0) }} ms (server {{ metrics.server.toFixed(2) }} ms)</span>
          </a-layout-footer>
        </a-layout>
      </a-layout>
      <a-modal v-model:open="loginOpen" title="Sign in" :footer="null" :width="380" destroy-on-close>
        <LoginForm @done="afterLogin" />
      </a-modal>
      <a-modal :open="session.setup" title="Create Administrator Account" ok-text="Create account" :closable="false" :mask-closable="false"
        :keyboard="false" :cancel-button-props="{ style: { display: 'none' } }" :confirm-loading="busy" @ok="createAdministrator">
        <a-alert type="info" show-icon style="margin-bottom: 16px" message="Proximity has no users yet. Create the first account to continue." />
        <a-form ref="setupForm" layout="vertical" :model="setup">
          <a-form-item label="User name" name="username" :rules="[{ required: true, pattern: /^[a-z0-9][a-z0-9_.-]{0,31}$/, message: 'Lower-case letters, digits, _ . - (up to 32)' }]">
            <a-input v-model:value="setup.username" autocomplete="username" />
          </a-form-item>
          <a-form-item label="Role">
            <a-select value="administrator" disabled :options="[{ value: 'administrator', label: 'Administrator — all access' }]" />
          </a-form-item>
          <a-form-item label="Password" name="password" :rules="[{ required: true, min: 6, message: 'At least 6 characters' }]">
            <a-input-password v-model:value="setup.password" autocomplete="new-password" />
          </a-form-item>
          <a-form-item label="Repeat password" name="confirm" :rules="[{ validator: checkSetup }]">
            <a-input-password v-model:value="setup.confirm" autocomplete="new-password" />
          </a-form-item>
        </a-form>
      </a-modal>
      <a-modal v-model:open="passwordOpen" title="Change password" ok-text="Change" :confirm-loading="busy" destroy-on-close @ok="changePassword">
        <a-form ref="passwordForm" layout="vertical" :model="password">
          <a-form-item label="Current password" name="current" :rules="[{ required: true, message: 'Enter current password' }]">
            <a-input-password v-model:value="password.current" autocomplete="current-password" />
          </a-form-item>
          <a-form-item label="New password" name="password" :rules="[{ required: true, min: 6, message: 'At least 6 characters' }]">
            <a-input-password v-model:value="password.password" autocomplete="new-password" />
          </a-form-item>
          <a-form-item label="Repeat new password" name="repeat" :rules="[{ validator: checkRepeat }]">
            <a-input-password v-model:value="password.repeat" autocomplete="new-password" />
          </a-form-item>
        </a-form>
      </a-modal>
    </a-layout>
  </a-config-provider>
</template>

<script setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { message, theme as antTheme } from 'ant-design-vue'
import {
  ArrowDownOutlined, ArrowUpOutlined, BulbOutlined, DashboardOutlined, ExportOutlined, GatewayOutlined,
  ImportOutlined, KeyOutlined, LoginOutlined, LogoutOutlined, SettingOutlined, SwapOutlined, TeamOutlined,
  UserOutlined
} from '@ant-design/icons-vue'
import { api, events, loadSession, metrics, session } from './api'
import { rate } from './format'
import Logo from './components/Logo.vue'
import LoginForm from './components/LoginForm.vue'
import Dashboard from './views/Dashboard.vue'
import Outbounds from './views/Outbounds.vue'
import Routes from './views/Routes.vue'
import Inbounds from './views/Inbounds.vue'
import Users from './views/Users.vue'
import Settings from './views/Settings.vue'

const sections = [
  { key: 'dashboard', title: 'Dashboard', icon: DashboardOutlined, view: Dashboard, level: 0 },
  { key: 'outbounds', title: 'Outbounds', icon: ExportOutlined, view: Outbounds, level: 1 },
  { key: 'routes', title: 'Routes', icon: SwapOutlined, view: Routes, level: 1 },
  { key: 'inbounds', title: 'Inbounds', icon: ImportOutlined, view: Inbounds, level: 1 },
  { key: 'users', title: 'Users', icon: TeamOutlined, view: Users, level: 3 },
  { key: 'settings', title: 'Settings', icon: SettingOutlined, view: Settings, level: 1 }
]
const aliases = { endpoints: 'outbounds', filters: 'routes', fallbacks: 'routes' }
const roleColors = { administrator: 'red', operator: 'blue', user: 'green' }
const year = new Date().getFullYear()
const dark = ref(localStorage.getItem('proximity-theme') === 'dark')
const collapsed = ref(false)
const section = ref('dashboard')
const overview = ref(null)
const loginOpen = ref(false)
const passwordOpen = ref(false)
const busy = ref(false)
const passwordForm = ref()
const password = reactive({ current: '', password: '', repeat: '' })
const setupForm = ref()
const setup = reactive({ username: 'admin', password: '', confirm: '' })
let timer = null

const theme = computed(() => ({
  algorithm: dark.value ? antTheme.darkAlgorithm : antTheme.defaultAlgorithm,
  token: { colorPrimary: '#0e9f97', borderRadius: 8 }
}))
const visibleSections = computed(() => sections.filter((item) => session.level >= item.level))
const locked = computed(() => session.level === 0 && !session.anonymous_dashboard)
const currentView = computed(() => {
  const found = visibleSections.value.find((item) => item.key === section.value)
  return (found || sections[0]).view
})
const routerBadge = computed(() => {
  if (!overview.value || !overview.value.router.running) return 'error'
  return overview.value.failure ? 'warning' : 'success'
})

function navigate(key) {
  section.value = key
  window.location.hash = `#/${key}`
}

function readHash() {
  const hash = window.location.hash.replace(/^#\/?/, '')
  const key = aliases[hash] || hash
  section.value = visibleSections.value.some((item) => item.key === key) ? key : 'dashboard'
}

function toggleTheme() {
  dark.value = !dark.value
  localStorage.setItem('proximity-theme', dark.value ? 'dark' : 'light')
}

async function refreshOverview() {
  if (locked.value) {
    overview.value = null
    return
  }
  try {
    overview.value = await api.get('/api/dashboard', section.value !== 'dashboard')
  } catch (error) {
    overview.value = overview.value || null
  }
}

async function afterLogin() {
  loginOpen.value = false
  readHash()
  await refreshOverview()
}

async function logout() {
  await api.post('/api/logout')
  await loadSession()
  section.value = 'dashboard'
  await refreshOverview()
}

async function checkRepeat(rule, value) {
  if (value !== password.password) {
    throw new Error('Passwords do not match')
  }
}

async function checkSetup(rule, value) {
  if (value !== setup.password) {
    throw new Error('Passwords do not match')
  }
}

async function createAdministrator() {
  try {
    await setupForm.value.validate()
  } catch (failure) {
    return
  }
  busy.value = true
  try {
    await api.post('/api/setup', setup)
    Object.assign(setup, { password: '', confirm: '' })
    await loadSession()
    readHash()
    await refreshOverview()
    message.success('Administrator account created')
  } catch (error) {
    message.error(error.message)
  } finally {
    busy.value = false
  }
}

async function changePassword() {
  try {
    await passwordForm.value.validate()
  } catch (failure) {
    return
  }
  busy.value = true
  try {
    await api.post('/api/password', { current: password.current, password: password.password, confirm: password.repeat })
    Object.assign(password, { current: '', password: '', repeat: '' })
    passwordOpen.value = false
    message.success('Password changed, sign in again')
    await loadSession()
    section.value = 'dashboard'
    loginOpen.value = true
  } catch (error) {
    message.error(error.message)
  } finally {
    busy.value = false
  }
}

onMounted(async () => {
  events.unauthorized = async () => {
    await loadSession()
    readHash()
  }
  await loadSession()
  readHash()
  window.addEventListener('hashchange', readHash)
  await refreshOverview()
  timer = setInterval(refreshOverview, 3000)
})

onUnmounted(() => {
  clearInterval(timer)
  window.removeEventListener('hashchange', readHash)
})
</script>

<style scoped>
.shell { min-height: 100vh; }
.header { display: flex; align-items: center; gap: 24px; padding: 0 20px; color: #e6f4f3; }
.brand { display: flex; align-items: center; gap: 10px; min-width: 170px; }
.brand-name { font-size: 20px; font-weight: 700; letter-spacing: .5px; color: #fff; }
.header-stats { display: flex; gap: 20px; flex: 1; font-size: 13px; white-space: nowrap; overflow: hidden; }
.header-stats .stat { display: inline-flex; align-items: center; gap: 6px; color: #b9d8d6; }
.header-stats :deep(.ant-badge-status-text) { display: none; }
.header-user { display: flex; align-items: center; gap: 8px; margin-left: auto; }
.header-button { color: #e6f4f3 !important; }
.content { padding: 24px; }
.footer { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 8px; padding: 12px 24px; font-size: 12px; opacity: .75; }
</style>
