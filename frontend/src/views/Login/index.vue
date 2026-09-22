<script setup lang="ts">
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Message } from '@arco-design/web-vue'
import { useAuthStore } from '@/store/auth'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()

const username = ref('')
const password = ref('')
const submitting = ref(false)

async function onSubmit(): Promise<void> {
  if (!username.value || !password.value) {
    Message.warning('请输入用户名与口令')
    return
  }
  submitting.value = true
  try {
    await auth.login(username.value, password.value)
    const redirect = (route.query.redirect as string | undefined) ?? '/dashboard'
    await router.replace(redirect)
  } catch (e) {
    const err = e as Error & { code?: string }
    // 429 是登录失败过多被节流，文案要提示等待而不是让用户继续试
    if ((e as { status?: number }).status === 429) {
      Message.error('登录失败次数过多，请稍后再试')
    } else {
      Message.error(err.message || '登录失败')
    }
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="brand">
        <div class="logo">MW</div>
        <div>
          <h1>MetalWatch</h1>
          <p>裸金属服务器硬件监控管控平台</p>
        </div>
      </div>

      <a-form :model="{ username, password }" layout="vertical" @submit-success="onSubmit">
        <a-form-item field="username" label="用户名" :rules="[{ required: true, message: '请输入用户名' }]">
          <a-input v-model="username" placeholder="admin" allow-clear autocomplete="username" />
        </a-form-item>
        <a-form-item field="password" label="口令" :rules="[{ required: true, message: '请输入口令' }]">
          <a-input-password v-model="password" placeholder="请输入口令" allow-clear autocomplete="current-password"
            @press-enter="onSubmit" />
        </a-form-item>
        <a-button type="primary" long :loading="submitting" @click="onSubmit">登录</a-button>
      </a-form>

      <p class="hint">
        首次启动时系统会在数据目录生成 <code>bootstrap_admin.txt</code>，
        内含初始管理员口令，登录后请立即修改。
      </p>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-fill-1);
}

.login-card {
  width: 380px;
  padding: 32px;
  border-radius: 8px;
  background: var(--color-bg-2);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.08);
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 24px;
}

.logo {
  width: 40px;
  height: 40px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  color: #fff;
  background: rgb(var(--primary-6));
}

.brand h1 {
  margin: 0;
  font-size: 18px;
}

.brand p {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--color-text-3);
}

.hint {
  margin: 16px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-3);
}

.hint code {
  padding: 0 4px;
  background: var(--color-fill-2);
  border-radius: 3px;
}
</style>
