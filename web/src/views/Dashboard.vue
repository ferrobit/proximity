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
        <a-col :xs="12" :md="8" class="stat-col">
          <a-card size="small" class="stat-card"><a-statistic title="Outbounds" :value="overview.counts.outbounds_up" :suffix="`/ ${overview.counts.outbounds}`">
            <template #prefix><ExportOutlined /></template></a-statistic>
            <div class="muted">{{ overview.counts.outbounds_enabled }} enabled</div></a-card>
        </a-col>
        <a-col :xs="12" :md="8" class="stat-col">
          <a-card size="small" class="stat-card"><a-statistic title="Routes" :value="overview.counts.routes">
            <template #prefix><SwapOutlined /></template></a-statistic>
            <div class="muted">{{ overview.counts.rules }} active rules</div></a-card>
        </a-col>
        <a-col :xs="12" :md="8" class="stat-col">
          <a-card size="small" class="stat-card"><a-statistic title="Uploaded" :value="bytes(overview.total.upload)">
            <template #prefix><ArrowUpOutlined /></template></a-statistic>
            <div class="muted">{{ rate(overview.total.upload_rate) }}</div></a-card>
        </a-col>
        <a-col :xs="12" :md="8" class="stat-col">
          <a-card size="small" class="stat-card"><a-statistic title="Downloaded" :value="bytes(overview.total.download)">
            <template #prefix><ArrowDownOutlined /></template></a-statistic>
            <div class="muted">{{ rate(overview.total.download_rate) }}</div></a-card>
        </a-col>
        <a-col :xs="12" :md="8" class="stat-col">
          <a-card size="small" class="stat-card"><a-statistic title="Router" :value="overview.router.running ? 'running' : 'stopped'"
            :value-style="{ color: overview.router.running ? '#3f8600' : '#cf1322' }">
            <template #prefix><DeploymentUnitOutlined /></template></a-statistic>
            <div class="muted">up {{ duration(overview.uptime) }}</div></a-card>
        </a-col>
      </a-row>
      <div class="charts">
        <TrafficChart v-model:range="range" class="chart-main" :overview="overview" :samples="history.samples.value" :step="history.step.value" />
        <ConnectionsChart class="chart-side" :samples="history.samples.value" :step="history.step.value" :range="range" />
      </div>
      <a-card size="small">
        <template #title><ExportOutlined /> Outbounds</template>
        <a-table :columns="outboundColumns" :data-source="overview.outbounds" row-key="id" size="small" :pagination="false" :scroll="{ x: 900 }">
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
import { ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  ArrowDownOutlined, ArrowUpOutlined, ClearOutlined, DashboardOutlined, DeploymentUnitOutlined, ExportOutlined,
  StopOutlined, SwapOutlined
} from '@ant-design/icons-vue'
import { api, canEdit, session } from '../api'
import { ago, bytes, duration, rate, reasonColors, typeColors, typeLabels, usageColors } from '../format'
import StateTag from '../components/StateTag.vue'
import TrafficChart from '../components/TrafficChart.vue'
import ConnectionsChart from '../components/ConnectionsChart.vue'
import { useHistory } from '../components/history'

const props = defineProps({ overview: { type: Object, default: null } })
const emit = defineEmits(['refresh'])
const range = ref(900)
const history = useHistory(() => props.overview)

const outboundColumns = [
  { title: 'Outbound', key: 'id' },
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

async function clearRefused() {
  try {
    await api.remove('/api/refused')
    emit('refresh')
  } catch (error) {
    message.error(error.message)
  }
}
</script>

<style scoped>
.charts { display: flex; gap: 16px; align-items: stretch; }
.chart-main { flex: 1 1 0; min-width: 0; }
.chart-side { flex: 0 0 30%; min-width: 0; }
@media (max-width: 991px) {
  .charts { flex-direction: column; }
  .chart-side { flex-basis: auto; }
}
@media (min-width: 1200px) {
  .stat-col { flex: 0 0 20%; max-width: 20%; }
}
</style>
