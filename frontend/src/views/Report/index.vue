<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Message } from '@arco-design/web-vue'
import { createReport } from '@/api/report'
import { listHosts } from '@/api/host'
import type { Host, ReportFormat, ReportJob, ReportKind } from '@/types/api'

// 报表中心：硬件巡检报告、资产清单导出。
const submitting = ref(false)
const job = ref<ReportJob | null>(null)
const hosts = ref<Host[]>([])

const form = reactive({
  kind: 'inventory' as ReportKind,
  format: 'xlsx' as ReportFormat,
  from: '',
  to: '',
  host_ids: [] as number[]
})

const kindOptions = [
  { label: '硬件巡检报告', value: 'inspection' },
  { label: '资产清单', value: 'inventory' }
]
const formatOptions = [
  { label: 'XLSX', value: 'xlsx' },
  { label: 'CSV', value: 'csv' },
  { label: 'JSON', value: 'json' },
  { label: 'HTML', value: 'html' }
]
const hostOptions = ref<{ label: string; value: number }[]>([])

async function loadHosts() {
  try {
    const resp = await listHosts({ limit: 500, offset: 0 })
    hosts.value = resp.items
    hostOptions.value = resp.items.map((h) => ({ label: `${h.hostname} (${h.primary_ip})`, value: h.id }))
  } catch (e) {
    Message.error((e as Error).message || '加载主机失败')
  }
}

async function generate() {
  submitting.value = true
  job.value = null
  try {
    const created = await createReport({
      kind: form.kind,
      format: form.format,
      from: form.from || undefined,
      to: form.to || undefined,
      host_ids: form.host_ids.length ? form.host_ids : undefined
    })
    job.value = created
    Message.success('报表已生成')
  } catch (e) {
    Message.error((e as Error).message || '生成失败')
  } finally {
    submitting.value = false
  }
}

function quickInventory() {
  form.kind = 'inventory'
  form.format = 'xlsx'
  form.host_ids = []
  void generate()
}
function quickInspect() {
  form.kind = 'inspection'
  form.format = 'html'
  form.host_ids = []
  void generate()
}

onMounted(loadHosts)
</script>

<template>
  <div class="page">
    <a-card :bordered="false">
      <a-space class="quick">
        <a-button type="primary" :loading="submitting" @click="quickInventory">导出资产清单</a-button>
        <a-button :loading="submitting" @click="quickInspect">生成巡检报告</a-button>
      </a-space>
    </a-card>

    <a-card :bordered="false" title="报表配置">
      <a-form :model="form" layout="vertical" class="form">
        <a-row :gutter="16">
          <a-col :xs="24" :md="12">
            <a-form-item label="报表类型">
              <a-select v-model="form.kind" :options="kindOptions" />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item label="导出格式">
              <a-select v-model="form.format" :options="formatOptions" />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item label="起始时间">
              <a-date-picker v-model="form.from" show-time format="YYYY-MM-DD HH:mm:ss" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :md="12">
            <a-form-item label="结束时间">
              <a-date-picker v-model="form.to" show-time format="YYYY-MM-DD HH:mm:ss" style="width: 100%" />
            </a-form-item>
          </a-col>
          <a-col :xs="24">
            <a-form-item label="指定主机（留空=全部）">
              <a-select v-model="form.host_ids" :options="hostOptions" multiple :max-tag-count="3" placeholder="全部主机" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-button type="primary" :loading="submitting" @click="generate">生成报表</a-button>
      </a-form>
    </a-card>

    <a-card v-if="job" :bordered="false" title="生成结果">
      <a-descriptions :column="2" bordered>
        <a-descriptions-item label="任务 ID">{{ job.id }}</a-descriptions-item>
        <a-descriptions-item label="状态">
          <a-tag :color="job.status === 'done' ? 'green' : job.status === 'failed' ? 'red' : 'orange'">{{ job.status }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="类型">{{ job.kind }}</a-descriptions-item>
        <a-descriptions-item label="格式">{{ job.format }}</a-descriptions-item>
      </a-descriptions>
      <a-button v-if="job.download_url" class="sm" type="outline" status="success">
        <a :href="job.download_url" target="_blank">下载报表</a>
      </a-button>
    </a-card>
  </div>
</template>

<style scoped>
.page {
  display: grid;
  gap: 16px;
}
.quick {
  margin-bottom: 4px;
}
.form {
  max-width: 880px;
}
.sm {
  margin-top: 12px;
}
</style>
