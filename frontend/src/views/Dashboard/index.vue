<script setup lang="ts">
// 监控大盘页面骨架（路由已接入，业务实现在对应工作流中补齐）
// 对 W8：服务器总数/在线/离线/告警统计与告警趋势；数据来自 GET /api/v1/overview
</script>

<template>
  <section class="mw-card">
    <h2 style="margin: 0 0 8px; font-size: 16px">监控大盘</h2>
    <p style="margin: 0; color: var(--mw-text-dim)">对 W8：服务器总数/在线/离线/告警统计与告警趋势；数据来自 GET /api/v1/overview</p>
    <p style="margin: 12px 0 0; color: var(--mw-text-dim); font-size: 12px">
      页面骨架已就绪：路由 /dashboard 可在浏览器中直接访问，接口接入后即可渲染真实数据。
    </p>
  </section>
</template>
