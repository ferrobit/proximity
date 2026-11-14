<template>
  <div class="stack">
    <h2 class="pane-title"><DashboardOutlined /> Dashboard</h2>
    <a-skeleton v-if="!overview" active />
    <template v-else>
      <a-alert v-if="overview.failure" type="error" show-icon message="Router configuration is not applied" :description="overview.failure" />
      <a-alert v-if="overview.warnings.length" type="warning" show-icon message="Router warnings">
        <template #description>
          <div v-for="line in overview.warnings" :key="line">{{ line }}</div>
        </template>
      </a-alert>
      <a-row :gutter="[16, 16]">
        <a-col :xs="12" :md="8" :xl="4">
          <a-card size="small"><a-statistic title="Endpoints" :value="overview.counts.endpoints_up" :suffix="`/ ${overview.counts.endpoints}`">
            <template #prefix><ApiOutlined /></template></a-statistic>
            <div class="muted">{{ overview.counts.endpoints_enabled }} enabled</div></a-card>
        </a-col>
        <a-col :xs="12" :md="8" :xl="4">
          <a-card size="small"><a-statistic title="Fallbacks" :value="overview.counts.fallbacks">
            <template #prefix><BranchesOutlined /></template></a-statistic>
            <div class="muted">proxy groups</div></a-card>
        </a-col>
        <a-col :xs="12" :md="8" :xl="4">
          <a-card size="small"><a-statistic title="Filters" :value="overview.counts.filters">
            <template #prefix><FilterOutlined /></template></a-statistic>
            <div class="muted">{{ overview.counts.rules }} active rules</div></a-card>
        </a-col>
        <a-col :xs="12" :md="8" :xl="4">
          <a-card size="small"><a-statistic title="Uploaded" :value="bytes(overview.total.upload)">
            <template #prefix><ArrowUpOutlined /></template></a-statistic>
            <div class="muted">{{ rate(overview.total.upload_rate) }}</div></a-card>
        </a-col>
        <a-col :xs="12" :md="8" :xl="4">
          <a-card size="small"><a-statistic title="Downloaded" :value="bytes(overview.total.download)">
            <template #prefix><ArrowDownOutlined /></template></a-statistic>
            <div class="muted">{{ rate(overview.total.download_rate) }}</div></a-card>
        </a-col>
        <a-col :xs="12" :md="8" :xl="4">
          <a-card size="small"><a-statistic title="Router" :value="overview.router.running ? 'running' : 'stopped'"
            :value-style="{ color: overview.router.running ? '#3f8600' : '#cf1322' }">
            <template #prefix><DeploymentUnitOutlined /></template></a-statistic>
            <div class="muted">mihomo {{ overview.router.version }} · port {{ overview.proxy_port }} · up {{ duration(overview.uptime) }}</div></a-card>
        </a-col>
      </a-row>
      <a-card size="small">
        <template #title><ApiOutlined /> Endpoints</template>
        <a-table :columns="endpointColumns" :data-source="overview.endpoints" row-key="id" size="small" :pagination="false" :scroll="{ x: 900 }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'id'">
              <b>{{ record.id }}</b>
              <div class="muted" v-if="record.name">{{ record.name }}</div>
            </template>
            <template v-else-if="column.key === 'type'">
              <a-tag :color="typeColors[record.type]">{{ typeLabels[record.type] }}</a-tag>
            </template>
            <template v-else-if="column.key === 'usage'">
              <a-tag :color="usageColors[record.usage]">{{ record.usage }}</a-tag>
            </template>
            <template v-else-if="column.key === 'state'"><StateTag :status="record.status" /></template>
            <template v-else-if="column.key === 'link'">
              <span v-if="record.status.link">
                <a-badge :status="record.status.link === 'up' ? 'success' : 'error'" :text="record.status.link" />
                <span class="muted mono" v-if="record.status.device" style="margin-left: 6px">{{ record.status.device }}</span>
              </span>
              <span v-else class="muted">—</span>
            </template>
            <template v-else-if="column.key === 'latency'">
              <span v-if="record.status.latency >= 0">{{ record.status.latency }} ms</span>
              <span v-else class="muted">—</span>
            </template>
            <template v-else-if="column.key === 'traffic'">
              <span v-if="record.statistics" class="nowrap">
                <ArrowUpOutlined /> {{ bytes(record.traffic.upload) }} <ArrowDownOutlined /> {{ bytes(record.traffic.download) }}
              </span>
              <span v-else class="muted">off</span>
            </template>
            <template v-else-if="column.key === 'rate'">
              <span v-if="record.statistics" class="nowrap">
                {{ rate(record.traffic.upload_rate) }} / {{ rate(record.traffic.download_rate) }}
                <span class="muted">({{ record.traffic.connections }})</span>
              </span>
              <span v-else class="muted">—</span>
            </template>
            <template v-else-if="column.key === 'since'">{{ ago(record.status.since) }}</template>
          </template>
        </a-table>
      </a-card>
      <a-card size="small" v-if="overview.fallbacks.length">
        <template #title><BranchesOutlined /> Fallbacks</template>
        <div v-for="fallback in overview.fallbacks" :key="fallback.id" class="chain" style="margin-bottom: 8px">
          <b style="min-width: 140px">{{ fallback.id }}</b>
          <a-tag v-if="!fallback.enabled">disabled</a-tag>
          <template v-for="(member, index) in fallback.endpoints" :key="member">
            <RightOutlined v-if="index" class="muted" />
            <a-tag :color="memberColor(member)">{{ member }}</a-tag>
          </template>
          <span v-if="!fallback.endpoints.length" class="muted">no endpoints</span>
        </div>
      </a-card>
      <a-card size="small" v-if="session.level > 0">
        <template #title><StopOutlined /> Unreachable routes</template>
        <template #extra>
          <a-popconfirm v-if="canEdit()" title="Clear the list?" @confirm="clearRefused">
            <a-button size="small" :disabled="!overview.refused.length"><template #icon><ClearOutlined /></template>Clear</a-button>
          </a-popconfirm>
        </template>
        <a-table :columns="refusedColumns" :data-source="overview.refused" size="small" :pagination="{ pageSize: 10, hideOnSinglePage: true }"
          :row-key="(record) => record.host + record.port + record.target" :locale="{ emptyText: 'No refused or timed out connections' }">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'host'"><span class="mono">{{ record.host }}<span v-if="record.port">:{{ record.port }}</span></span></template>
            <template v-else-if="column.key === 'reason'"><a-tag :color="reasonColors[record.reason]">{{ record.reason }}</a-tag></template>
            <template v-else-if="column.key === 'rule'"><span class="mono">{{ record.rule }}</span></template>
            <template v-else-if="column.key === 'last'">{{ ago(record.last) }}</template>
          </template>
        </a-table>
      </a-card>
    </template>
  </div>
</template>

<script setup>
import { message } from 'ant-design-vue'
import {
  ApiOutlined, ArrowDownOutlined, ArrowUpOutlined, BranchesOutlined, ClearOutlined, DashboardOutlined,
  DeploymentUnitOutlined, FilterOutlined, RightOutlined, StopOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, session } from '../api'
import { ago, bytes, duration, rate, reasonColors, typeColors, typeLabels, usageColors } from '../format'
import StateTag from '../components/StateTag.vue'

const props = defineProps({ overview: { type: Object, default: null } })
const emit = defineEmits(['refresh'])

const endpointColumns = [
  { title: 'Endpoint', key: 'id' },
  { title: 'Type', key: 'type' },
  { title: 'Group', dataIndex: 'group' },
  { title: 'Status', key: 'usage' },
  { title: 'Operation', key: 'state' },
  { title: 'Link', key: 'link' },
  { title: 'Latency', key: 'latency' },
  { title: 'Traffic', key: 'traffic' },
  { title: 'Rate (connections)', key: 'rate' },
  { title: 'Since', key: 'since' }
]
const refusedColumns = [
  { title: 'Destination', key: 'host' },
  { title: 'Reason', key: 'reason' },
  { title: 'Via', dataIndex: 'target' },
  { title: 'Matched rule', key: 'rule' },
  { title: 'Count', dataIndex: 'count' },
  { title: 'Last seen', key: 'last' }
]

function memberColor(member) {
  const found = props.overview.endpoints.find((item) => item.id === member)
  if (!found || !found.enabled) return 'default'
  return found.status.state === 'up' ? 'green' : found.status.state === 'failed' || found.status.state === 'down' ? 'red' : 'orange'
}

async function clearRefused() {
  try {
    await api.remove('/api/refused')
    emit('refresh')
  } catch (error) {
    message.error(error.message)
  }
}
</script>
