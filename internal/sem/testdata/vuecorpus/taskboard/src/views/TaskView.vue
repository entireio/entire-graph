<script setup lang="ts">
import { computed, onMounted, provide } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import TaskDetailPanel from '@/components/TaskDetailPanel.vue'
import { useAuthStore } from '@/stores/auth'
import { useProjectStore } from '@/stores/projects'
import { useTaskStore } from '@/stores/tasks'
import { BoardContextKey } from '@/keys'
import type { Task } from '@/types/task'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { board } = storeToRefs(useProjectStore())
const { findTask, loadTasks } = useTaskStore()

const taskId = computed(() => String(route.params.taskId))
const task = computed(() => findTask(taskId.value) ?? null)

function goToTask(next: Task) {
  router.push({ name: 'task', params: { taskId: next.id } })
}

function backToBoard() {
  if (task.value) {
    router.push({ name: 'board', params: { projectId: task.value.projectId } })
  } else {
    router.back()
  }
}

async function ensureLoaded() {
  if (!task.value && typeof route.query.project === 'string') {
    await loadTasks(route.query.project)
  }
}

provide(BoardContextKey, {
  board,
  canEdit: computed(() => auth.isAdmin),
  openTask: goToTask,
})

onMounted(ensureLoaded)
</script>

<template>
  <div id="task-page" class="task-view">
    <BaseButton variant="ghost" @click="backToBoard">Back to board</BaseButton>
    <TaskDetailPanel v-if="task" :task-id="taskId" @close="backToBoard" />
    <p v-else class="task-view__missing">This task could not be found.</p>
  </div>
</template>
