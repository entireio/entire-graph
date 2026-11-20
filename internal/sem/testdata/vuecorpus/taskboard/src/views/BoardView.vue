<script setup lang="ts">
import { computed, onMounted, provide, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import BoardColumn from '@/components/BoardColumn.vue'
import TaskDetailPanel from '@/components/TaskDetailPanel.vue'
import TaskFilters from '@/components/TaskFilters.vue'
import TaskForm from '@/components/TaskForm.vue'
import { useFilters } from '@/composables/useFilters'
import { useAuthStore } from '@/stores/auth'
import { useProjectStore } from '@/stores/projects'
import { useTaskStore } from '@/stores/tasks'
import { BoardContextKey } from '@/keys'
import type { Task, TaskDraft, TaskMovedPayload } from '@/types/task'

const props = defineProps<{ projectId: string }>()

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const taskStore = useTaskStore()
const projectStore = useProjectStore()
const { tasks, loading } = storeToRefs(taskStore)
const { board, currentProject, members } = storeToRefs(projectStore)
const { filters, filteredTasks, activeCount, setQuery, togglePriority, clearFilters } = useFilters(tasks)

const searchText = ref('')
const showForm = ref(false)
const taskForm = ref<InstanceType<typeof TaskForm> | null>(null)
const selectedTaskId = ref<string | null>(null)

const columns = computed(() => board.value?.columns ?? [])
const canEdit = computed(() => auth.isAdmin || currentProject.value?.members.some((member) => member.id === auth.currentUser?.id) === true)

function tasksFor(columnId: string): Task[] {
  return filteredTasks.value.filter((task) => task.columnId === columnId)
}

function openTask(task: Task) {
  selectedTaskId.value = task.id
  router.replace({ query: { ...route.query, task: task.id } })
}

function closeTask() {
  selectedTaskId.value = null
  router.replace({ query: { ...route.query, task: undefined } })
}

async function onTaskMoved(payload: TaskMovedPayload) {
  await taskStore.moveTask(payload)
}

async function onTaskToggled(taskId: string, done: boolean) {
  await taskStore.editTask(taskId, { status: done ? 'done' : 'todo' })
}

async function openNewTask() {
  showForm.value = true
  await taskForm.value?.focusTitle()
}

async function createFromDraft(draft: TaskDraft) {
  await taskStore.addTask(props.projectId, draft)
  showForm.value = false
  taskForm.value?.reset()
}

provide(BoardContextKey, { board, canEdit, openTask })

watch(searchText, (text) => setQuery(text))

watch(
  () => route.query.task,
  (taskId) => {
    selectedTaskId.value = typeof taskId === 'string' ? taskId : null
  },
  { immediate: true },
)

onMounted(async () => {
  await projectStore.selectProject(props.projectId)
  await taskStore.loadTasks(props.projectId)
})
</script>

<template>
  <div id="board-root" class="board-view">
    <header class="board-view__header">
      <h1>{{ currentProject?.name ?? 'Board' }}</h1>
      <TaskFilters
        v-model="searchText"
        :filters="filters"
        :active-count="activeCount"
        @toggle-priority="togglePriority"
        @clear="clearFilters"
      />
      <BaseButton v-if="canEdit" variant="primary" @click="openNewTask">New task</BaseButton>
    </header>
    <p v-if="loading" class="board-view__loading">Loading tasks...</p>
    <div v-else id="board-columns" class="board-view__columns">
      <BoardColumn
        v-for="column in columns"
        :key="column.id"
        :column="column"
        :tasks="tasksFor(column.id)"
        :selected-task-id="selectedTaskId"
        @task-moved="onTaskMoved"
        @task-selected="openTask"
        @task-toggled="onTaskToggled"
      >
        <template #title="{ column: col, count }">
          <h2 class="column-title">{{ col.title }} <small>({{ count }})</small></h2>
        </template>
      </BoardColumn>
    </div>
    <BaseModal v-model:open="showForm" title="New task">
      <TaskForm ref="taskForm" :members="members" @submit="createFromDraft" @cancel="showForm = false" />
    </BaseModal>
    <TaskDetailPanel v-if="selectedTaskId" :task-id="selectedTaskId" @close="closeTask" />
  </div>
</template>
