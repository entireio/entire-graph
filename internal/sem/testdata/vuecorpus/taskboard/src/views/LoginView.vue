<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import type { Credentials } from '../types/user'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()

const form = reactive<Credentials>({ email: '', password: '' })
const submitting = ref(false)
const errorMessage = ref<string | null>(null)

function redirectTarget(): string {
  return typeof route.query.redirect === 'string' ? route.query.redirect : '/'
}

async function handleLogin() {
  submitting.value = true
  errorMessage.value = null
  try {
    await auth.login({ ...form })
    await router.push(redirectTarget())
  } catch {
    errorMessage.value = auth.lastError ?? 'Unable to sign in'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <main class="login-view">
    <form id="login-form" class="login-view__form" @submit.prevent="handleLogin">
      <h1>Sign in to Taskboard</h1>
      <label for="login-email">Email</label>
      <input id="login-email" v-model="form.email" type="email" autocomplete="username" required />
      <label for="login-password">Password</label>
      <input id="login-password" v-model="form.password" type="password" autocomplete="current-password" required />
      <p v-if="errorMessage" class="error" role="alert">{{ errorMessage }}</p>
      <BaseButton type="submit" variant="primary" :loading="submitting">Sign in</BaseButton>
    </form>
  </main>
</template>
