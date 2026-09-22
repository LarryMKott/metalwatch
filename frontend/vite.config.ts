import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 说明：
//  - base 用相对路径，便于被飞牛桌面入口以 iframe 嵌入（可能挂在 /app/metalwatch 之类子路径）
//  - 构建产物由服务端 go:embed 打包，无需额外处理
//  - 开发期 /api 与 /ws 代理到本地服务端；生产同源直连
//  - 使用 Vite 5（rollup）。manualChunks 统一用函数形式最稳妥（Vite 8/rolldown 也只接受函数，
//    函数形式在 rollup 与 rolldown 下都可用，避免切换构建器时改写法）。
//  - engines.node 写成 ">=22.12.0 <25" 而非 ">=22 <=24"：后者语义上限只到 24.0.0，
//    会把 24.0.1+ 全部排除，与「兼容 Node 24」的意图相反（见 package.json）。
export default defineConfig({
  base: './',
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    assetsInlineLimit: 2048,
    chunkSizeWarningLimit: 1200,
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (id.includes('node_modules/echarts') || id.includes('node_modules/zrender')) return 'echarts'
          if (id.includes('node_modules/@arco-design')) return 'arco'
          if (id.includes('node_modules/vue') || id.includes('node_modules/@vue')) return 'vue'
          if (id.includes('node_modules/axios')) return 'axios'
          return undefined
        }
      }
    }
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:18080', changeOrigin: true, ws: true },
      '/healthz': { target: 'http://127.0.0.1:18080', changeOrigin: true }
    }
  }
})
