<script setup lang="ts">
// 存储管理页面骨架（路由已接入，业务实现在对应工作流中补齐）
// 对 W1：多存储后端切换、连通性测试、备份与迁移；数据来自 GET /api/v1/system/storage/backends
</script>

<template>
  <section class="mw-card">
    <h2 style="margin: 0 0 8px; font-size: 16px">存储管理</h2>
    <p style="margin: 0; color: var(--mw-text-dim)">对 W1：多存储后端切换、连通性测试、备份与迁移；数据来自 GET /api/v1/system/storage/backends</p>
    <p style="margin: 12px 0 0; color: var(--mw-text-dim); font-size: 12px">
      页面骨架已就绪：路由 /storage 可在浏览器中直接访问，接口接入后即可渲染真实数据。
    </p>
  </section>
</template>
