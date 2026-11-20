<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ page: number; pageCount: number }>()

const emit = defineEmits<{
  next: []
  prev: []
}>()

const atStart = computed(() => props.page <= 1)
const atEnd = computed(() => props.page >= props.pageCount)
</script>

<template>
  <nav class="pagination" aria-label="Pagination">
    <button type="button" :disabled="atStart" @click="emit('prev')">Previous</button>
    <span class="pagination__status">Page {{ page }} of {{ pageCount }}</span>
    <button type="button" :disabled="atEnd" @click="emit('next')">Next</button>
  </nav>
</template>
