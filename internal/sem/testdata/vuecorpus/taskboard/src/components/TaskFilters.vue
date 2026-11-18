<script setup lang="ts">
import { computed } from 'vue'
import type { Priority } from '@/types/task'
import type { TaskFilterState } from '@/composables/useFilters'

const props = defineProps<{ filters: TaskFilterState; activeCount: number }>()

const search = defineModel<string>({ default: '' })

const emit = defineEmits<{
  'toggle-priority': [priority: Priority]
  clear: []
}>()

const priorities: Priority[] = ['urgent', 'high', 'medium', 'low']
const hasFilters = computed(() => props.activeCount > 0 || search.value.length > 0)

function isActive(priority: Priority): boolean {
  return props.filters.priorities.includes(priority)
}

function clearAll() {
  search.value = ''
  emit('clear')
}
</script>

<template>
  <form class="task-filters" role="search" @submit.prevent>
    <label for="filter-search" class="visually-hidden">Search tasks</label>
    <input id="filter-search" v-model="search" type="search" placeholder="Search tasks" />
    <div class="task-filters__priorities" role="group" aria-label="Priority">
      <button
        v-for="priority in priorities"
        :key="priority"
        type="button"
        :class="{ active: isActive(priority) }"
        @click="emit('toggle-priority', priority)"
      >
        {{ priority }}
      </button>
    </div>
    <BaseButton v-if="hasFilters" variant="ghost" @click="clearAll">Clear</BaseButton>
  </form>
</template>
