<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'

const route = useRoute()
const router = useRouter()

function goHome() {
  router.replace({ name: 'projects' })
}
</script>

<template>
  <section id="not-found" class="not-found-view">
    <h1>Page not found</h1>
    <p>Nothing lives at <code>{{ route.fullPath }}</code>.</p>
    <BaseButton variant="primary" @click="goHome">Back to projects</BaseButton>
  </section>
</template>
