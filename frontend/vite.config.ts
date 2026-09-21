import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 说明：
//  - base 用相对路径，便于被飞牛桌面入口以 iframe 嵌入（可能挂在 /app/metalwatch 之类子路径）
//  - 构建产物由服务端 go:embed 打包，无需额外处理
//  - 开发期 /api 代理到本地服务端；生产同源直连
//  - Vite 8 默认使用 rolldown 打包，manualChunks 只接受**函数**形式（对象写法会直接构建失败）
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
    assetsInlineLimit: 2048,
    chunkSizeWarningLimit: 1200,
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (id.includes('node_modules/echarts') || id.includes('node_modules/zrender')) return 'echarts'
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
      '/api': { target: 'http://127.0.0.1:18080', changeOrigin: true },
      '/healthz': { target: 'http://127.0.0.1:18080', changeOrigin: true }
    }
  }
})
