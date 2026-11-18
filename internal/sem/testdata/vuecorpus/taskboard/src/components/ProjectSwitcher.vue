<script lang="ts">
import type { Project } from '@/types/project'

export interface SwitcherOption {
  value: string
  label: string
}

export function toOption(project: Project): SwitcherOption {
  return { value: project.id, label: `${project.key} - ${project.name}` }
}

export default {
  name: 'ProjectSwitcher',
  inheritAttrs: false,
}
</script>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useProjectStore } from '@/stores/projects'

withDefaults(defineProps<{ compact?: boolean }>(), { compact: false })

const router = useRouter()
const projectStore = useProjectStore()
const { activeProjects, currentProjectId } = storeToRefs(projectStore)
const { loadProjects, selectProject } = projectStore

const options = computed(() => activeProjects.value.map(toOption))

async function onChange(event: Event) {
  const projectId = (event.target as HTMLSelectElement).value
  await selectProject(projectId)
  await router.push({ name: 'board', params: { projectId } })
}

onMounted(() => {
  if (activeProjects.value.length === 0) {
    loadProjects()
  }
})
</script>

<template>
  <div class="project-switcher" :class="{ compact }" v-bind="$attrs">
    <label class="visually-hidden" for="project-select">Project</label>
    <select id="project-select" :value="currentProjectId ?? ''" @change="onChange">
      <option value="" disabled>Select a project</option>
      <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
    </select>
  </div>
</template>
