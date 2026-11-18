<script setup lang="ts">
import { useNotifications } from '@/composables/useNotifications'

const { notices, dismiss } = useNotifications()
</script>

<template>
  <TransitionGroup tag="ul" name="toast" class="notification-stack" aria-live="polite">
    <li v-for="notice in notices" :key="notice.id" :data-notice-id="notice.id" :class="['toast', `toast--${notice.tone}`]">
      <span>{{ notice.message }}</span>
      <button type="button" aria-label="Dismiss" @click="dismiss(notice.id)">x</button>
    </li>
  </TransitionGroup>
</template>
