<script setup lang="ts">
import { computed } from 'vue'
import type { User } from '@/types/user'
import { initials } from '@/utils/format'

const props = withDefaults(defineProps<{ user: User; size?: 'xs' | 'sm' | 'md' }>(), { size: 'md' })

const label = computed(() => initials(props.user.name))
</script>

<template>
  <span class="avatar" :class="`avatar--${size}`" :title="user.name">
    <img v-if="user.avatarUrl" :src="user.avatarUrl" :alt="user.name" />
    <template v-else>{{ label }}</template>
  </span>
</template>
