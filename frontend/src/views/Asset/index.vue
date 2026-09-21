<script setup lang="ts">
// 硬件资产页面骨架（路由已接入，业务实现在对应工作流中补齐）
// 对 W2：资产列表、检索、详情（部件树/SMART/RAID/变更记录）；数据来自 GET /api/v1/hosts
</script>

<template>
  <section class="mw-card">
    <h2 style="margin: 0 0 8px; font-size: 16px">硬件资产</h2>
    <p style="margin: 0; color: var(--mw-text-dim)">对 W2：资产列表、检索、详情（部件树/SMART/RAID/变更记录）；数据来自 GET /api/v1/hosts</p>
    <p style="margin: 12px 0 0; color: var(--mw-text-dim); font-size: 12px">
      页面骨架已就绪：路由 /asset 可在浏览器中直接访问，接口接入后即可渲染真实数据。
    </p>
  </section>
</template>
