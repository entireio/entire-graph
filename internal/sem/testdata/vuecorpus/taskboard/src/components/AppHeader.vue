<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useAuthStore } from '@/stores/auth'
import { useSettingsStore } from '@/stores/settings'
import ProjectSwitcher from './ProjectSwitcher.vue'
import AssigneeAvatar from './AssigneeAvatar.vue'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const { currentUser, isAuthenticated } = storeToRefs(auth)
const { toggleTheme } = useSettingsStore()

const onBoard = computed(() => route.name === 'board')

async function signOut() {
  await auth.logout()
  await router.push({ name: 'login' })
}

function goToSettings() {
  router.push('/settings')
}
</script>

<template>
  <header id="app-header" class="app-header">
    <RouterLink to="/" class="brand">Taskboard</RouterLink>
    <ProjectSwitcher v-if="isAuthenticated" :compact="onBoard" />
    <nav class="app-header__actions" aria-label="Account">
      <base-button variant="ghost" @click="toggleTheme">Theme</base-button>
      <BaseButton variant="ghost" @click="goToSettings">Settings</BaseButton>
      <AssigneeAvatar v-if="currentUser" :user="currentUser" size="sm" />
      <BaseButton v-if="isAuthenticated" variant="primary" @click="signOut">Sign out</BaseButton>
    </nav>
  </header>
</template>
