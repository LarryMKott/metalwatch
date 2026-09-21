<script setup lang="ts">
// 告警中心页面骨架（路由已接入，业务实现在对应工作流中补齐）
// 对 W7：活跃告警、历史检索、阈值模板与维护窗口；数据来自 GET /api/v1/alerts
</script>

<template>
  <section class="mw-card">
    <h2 style="margin: 0 0 8px; font-size: 16px">告警中心</h2>
    <p style="margin: 0; color: var(--mw-text-dim)">对 W7：活跃告警、历史检索、阈值模板与维护窗口；数据来自 GET /api/v1/alerts</p>
    <p style="margin: 12px 0 0; color: var(--mw-text-dim); font-size: 12px">
      页面骨架已就绪：路由 /alarm 可在浏览器中直接访问，接口接入后即可渲染真实数据。
    </p>
  </section>
</template>
