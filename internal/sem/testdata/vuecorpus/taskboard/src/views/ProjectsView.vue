<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useProjectStore } from '@/stores/projects'
import { useSettingsStore } from '@/stores/settings'
import { usePagination } from '@/composables/usePagination'
import PaginationBar from '@/components/PaginationBar.vue'
import type { Project } from '@/types/project'

const router = useRouter()
const projectStore = useProjectStore()
const { activeProjects } = storeToRefs(projectStore)
const { pageSize } = storeToRefs(useSettingsStore())
const { pageItems, page, pageCount, next, prev, goTo } = usePagination(activeProjects, pageSize)

const projectCount = computed(() => activeProjects.value.length)

async function openProject(project: Project) {
  await projectStore.selectProject(project.id)
  await router.push({ name: 'board', params: { projectId: project.id } })
}

onMounted(async () => {
  await projectStore.loadProjects()
  goTo(1)
})
</script>

<template>
  <section id="projects-page" class="projects-view">
    <h1>Projects <small>({{ projectCount }})</small></h1>
    <ul class="project-grid">
      <li v-for="project in pageItems" :key="project.id" :data-project-id="project.id">
        <button type="button" class="project-card" @click="openProject(project)">
          <span class="project-card__key">{{ project.key }}</span>
          <span class="project-card__name">{{ project.name }}</span>
        </button>
      </li>
    </ul>
    <pagination-bar :page="page" :page-count="pageCount" @next="next" @prev="prev" />
  </section>
</template>
