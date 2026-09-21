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
