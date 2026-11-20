<script setup lang="ts">
import { provide, watchEffect } from 'vue'
import { storeToRefs } from 'pinia'
import AppHeader from './components/AppHeader.vue'
import NotificationStack from '@/components/NotificationStack.vue'
import { useSettingsStore } from '@/stores/settings'
import { ThemeKey } from '@/keys'

const settings = useSettingsStore()
const { theme, density } = storeToRefs(settings)

provide(ThemeKey, theme)

watchEffect(() => {
  document.documentElement.dataset.theme = theme.value
})
</script>

<template>
  <div id="app-shell" class="app" :class="[`theme-${theme}`, `density-${density}`]">
    <AppHeader />
    <main id="main-content" class="app-main">
      <RouterView v-slot="{ Component }">
        <Transition name="fade" mode="out-in">
          <component :is="Component" />
        </Transition>
      </RouterView>
    </main>
    <NotificationStack />
  </div>
</template>

<style>
.app {
  min-height: 100vh;
}
</style>
