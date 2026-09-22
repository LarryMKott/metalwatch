import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ArcoVue from '@arco-design/web-vue'
import '@arco-design/web-vue/dist/arco.css'
import App from './App.vue'
import router from './router'
import './styles/main.css'

// 监控后台使用深色主题：Arco 通过 body[arco-theme='dark'] 切换暗色变量。
document.body.setAttribute('arco-theme', 'dark')

createApp(App).use(createPinia()).use(router).use(ArcoVue).mount('#app')

// W11：任意接口返回 401（令牌过期 / 被停用 / 账号已禁用）时统一回登录页。
// 令牌的清理在 request 拦截器里做，这里只负责跳转。
window.addEventListener('mw:unauthorized', () => {
  if (router.currentRoute.value.name !== 'login') {
    void router.replace('/login')
  }
})
