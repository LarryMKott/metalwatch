<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Message, type TableColumnData } from '@arco-design/web-vue'
import { getBmcCapability, listBmcAudit, sendBmcCommand } from '@/api/bmc'
import { listHosts } from '@/api/host'
import { formatTime } from '@/utils/format'
import type { BmcAuditEntry, BmcCapability, Host } from '@/types/api'

// BMC 管控：风扇调速、电源控制、指示灯定位、控制策略配置、操作审计。
const hosts = ref<Host[]>([])
const hostId = ref<number | undefined>(undefined)
const capability = ref<BmcCapability | null>(null)
const capabilityLoading = ref(false)
const sending = ref(false)

const fanSpeed = ref(50)
const fanAuto = ref(true)
const powerAction = ref<'on' | 'off' | 'reset' | 'cycle' | 'soft'>('reset')
const blinkDuration = ref(10)

const audit = ref<BmcAuditEntry[]>([])
const auditLoading = ref(false)
const auditColumns: TableColumnData[] = [
  { title: '时间', dataIndex: 'created_at', slotName: 'created_at' },
  { title: '主机', dataIndex: 'host_name', slotName: 'host' },
  { title: '操作', dataIndex: 'cmd_type' },
  { title: '目标', dataIndex: 'target' },
  { title: '结果', dataIndex: 'result', slotName: 'result' },
  { title: '操作人', dataIndex: 'operator' }
]

const hostOptions = computed(() => hosts.value.map((h) => ({ label: `${h.hostname} (${h.primary_ip})`, value: h.id })))

function formatSpeedTip(v: number): string {
  return `${v}%`
}

async function loadHosts() {
  try {
    const resp = await listHosts({ limit: 500, offset: 0 })
    hosts.value = resp.items
    if (!hostId.value && hosts.value.length) {
      hostId.value = hosts.value[0].id
      await loadCapability()
    }
  } catch (e) {
    Message.error((e as Error).message || '加载主机列表失败')
  }
}

async function loadCapability() {
  if (hostId.value === undefined) return
  capabilityLoading.value = true
  try {
    capability.value = await getBmcCapability(hostId.value)
  } catch (e) {
    capability.value = null
    Message.error((e as Error).message || '探测 BMC 能力失败')
  } finally {
    capabilityLoading.value = false
  }
}

async function applyFan() {
  if (hostId.value === undefined) return
  sending.value = true
  try {
    const r = await sendBmcCommand(hostId.value, {
      cmd_type: 'fan',
      speed_percent: fanAuto.value ? undefined : fanSpeed.value,
      auto_mode: fanAuto.value
    })
    Message.success(r.message || '风扇策略已下发')
    await loadAudit()
  } catch (e) {
    Message.error((e as Error).message || '下发失败')
  } finally {
    sending.value = false
  }
}
async function execPower() {
  if (hostId.value === undefined) return
  sending.value = true
  try {
    const r = await sendBmcCommand(hostId.value, { cmd_type: 'power', power_action: powerAction.value })
    Message.success(r.message || '电源指令已下发')
    await loadAudit()
  } catch (e) {
    Message.error((e as Error).message || '下发失败')
  } finally {
    sending.value = false
  }
}
async function blink() {
  if (hostId.value === undefined) return
  sending.value = true
  try {
    const r = await sendBmcCommand(hostId.value, { cmd_type: 'identify', duration_sec: blinkDuration.value })
    Message.success(r.message || '指示灯定位已触发')
    await loadAudit()
  } catch (e) {
    Message.error((e as Error).message || '下发失败')
  } finally {
    sending.value = false
  }
}
async function savePolicy() {
  if (hostId.value === undefined) return
  sending.value = true
  try {
    const r = await sendBmcCommand(hostId.value, { cmd_type: 'policy', target: 'thermal' })
    Message.success(r.message || '控制策略已保存')
    await loadAudit()
  } catch (e) {
    Message.error((e as Error).message || '保存失败')
  } finally {
    sending.value = false
  }
}
async function loadAudit() {
  auditLoading.value = true
  try {
    audit.value = await listBmcAudit()
  } catch (e) {
    audit.value = []
  } finally {
    auditLoading.value = false
  }
}

onMounted(() => {
  void loadHosts()
  void loadAudit()
})
</script>

<template>
  <div class="page">
    <a-card :bordered="false" class="toolbar">
      <a-space>
        <span class="lbl">目标主机</span>
        <a-select v-model="hostId" :options="hostOptions" style="width: 280px" @change="loadCapability" />
        <a-button type="outline" :loading="capabilityLoading" @click="loadCapability">探测能力</a-button>
      </a-space>
    </a-card>

    <a-spin :loading="capabilityLoading">
      <a-row :gutter="16">
        <a-col :xs="24" :lg="12">
          <a-card :bordered="false" title="BMC 能力">
            <a-descriptions v-if="capability" :column="1" bordered>
              <a-descriptions-item label="型号">{{ capability.bmc_model || '-' }}</a-descriptions-item>
              <a-descriptions-item label="固件版本">{{ capability.firmware_version || '-' }}</a-descriptions-item>
              <a-descriptions-item label="风扇控制">
                <a-tag :color="capability.fan_control ? 'green' : 'gray'">{{ capability.fan_control ? '支持' : '不支持' }}</a-tag>
              </a-descriptions-item>
              <a-descriptions-item label="电源控制">
                <a-tag :color="capability.power_control ? 'green' : 'gray'">{{ capability.power_control ? '支持' : '不支持' }}</a-tag>
              </a-descriptions-item>
              <a-descriptions-item label="指示灯定位">
                <a-tag :color="capability.identify ? 'green' : 'gray'">{{ capability.identify ? '支持' : '不支持' }}</a-tag>
              </a-descriptions-item>
            </a-descriptions>
            <a-empty v-else description="请选择主机并探测能力" />
          </a-card>
        </a-col>

        <a-col :xs="24" :lg="12">
          <a-card :bordered="false" title="风扇调速">
            <a-space direction="vertical" fill>
              <a-switch v-model="fanAuto"><template #checked>自动</template><template #unchecked>手动</template></a-switch>
              <a-slider v-model="fanSpeed" :disabled="fanAuto" :min="10" :max="100" :format-tooltip="formatSpeedTip" />
              <a-button type="primary" :disabled="!capability?.fan_control" :loading="sending" @click="applyFan">应用风扇策略</a-button>
            </a-space>
          </a-card>
        </a-col>

        <a-col :xs="24" :lg="12">
          <a-card :bordered="false" title="电源控制">
            <a-space direction="vertical" fill>
              <a-radio-group v-model="powerAction" type="button">
                <a-radio value="on">开机</a-radio>
                <a-radio value="off">关机</a-radio>
                <a-radio value="reset">硬复位</a-radio>
                <a-radio value="cycle">电源循环</a-radio>
                <a-radio value="soft">软关机</a-radio>
              </a-radio-group>
              <a-button :disabled="!capability?.power_control" :loading="sending" @click="execPower">执行电源指令</a-button>
            </a-space>
          </a-card>
        </a-col>

        <a-col :xs="24" :lg="12">
          <a-card :bordered="false" title="指示灯定位">
            <a-space direction="vertical" fill>
              <a-input-number v-model="blinkDuration" :min="1" :max="300" :default-value="10">
                <template #prefix>时长(秒)</template>
              </a-input-number>
              <a-button :disabled="!capability?.identify" :loading="sending" @click="blink">触发定位闪烁</a-button>
            </a-space>
          </a-card>
        </a-col>

        <a-col :xs="24" :lg="12">
          <a-card :bordered="false" title="控制策略配置">
            <a-space direction="vertical" fill>
              <p class="dim">基于温度阈值的风扇策略、电源冗余策略等，保存后由 BMC 持久化。</p>
              <a-button :loading="sending" @click="savePolicy">保存控制策略</a-button>
            </a-space>
          </a-card>
        </a-col>
      </a-row>
    </a-spin>

    <a-card :bordered="false" title="操作审计">
      <a-table :data="audit" :columns="auditColumns" :loading="auditLoading" :pagination="false" row-key="id">
        <template #created_at="{ record }">{{ formatTime((record as BmcAuditEntry).created_at) }}</template>
        <template #host="{ record }">{{ (record as BmcAuditEntry).host_name || '-' }}</template>
        <template #result="{ record }">
          <a-tag :color="(record as BmcAuditEntry).result === 'success' ? 'green' : 'red'">
            {{ (record as BmcAuditEntry).result }}
          </a-tag>
        </template>
      </a-table>
    </a-card>
  </div>
</template>

<style scoped>
.page {
  display: grid;
  gap: 16px;
}
.toolbar {
  padding: 12px 16px;
}
.lbl {
  color: var(--mw-text-dim);
}
.dim {
  color: var(--mw-text-dim);
  font-size: 12px;
}
</style>
