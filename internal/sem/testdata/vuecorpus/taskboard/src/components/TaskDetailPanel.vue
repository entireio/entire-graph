<script setup lang="ts">
import { computed, defineAsyncComponent, inject } from 'vue'
import { useTaskStore } from '@/stores/tasks'
import { BoardContextKey } from '@/keys'
import { relativeDueLabel } from '@/utils/format'
import LabelBadge from './LabelBadge.vue'

const CommentList = defineAsyncComponent(() => import('./CommentList.vue'))

const props = defineProps<{ taskId: string }>()

const emit = defineEmits<{
  close: []
}>()

const board = inject(BoardContextKey)
if (!board) {
  throw new Error('TaskDetailPanel must be rendered inside a board')
}

const store = useTaskStore()
const task = computed(() => store.findTask(props.taskId) ?? null)

function dueText(): string {
  return task.value ? relativeDueLabel(task.value.dueDate) : ''
}

async function removeLabel(labelId: string) {
  if (!task.value) {
    return
  }
  await store.editTask(task.value.id, { labels: task.value.labels.filter((label) => label.id !== labelId) })
}

async function deleteCurrent() {
  if (!task.value) {
    return
  }
  await store.removeTask(task.value.id)
  emit('close')
}
</script>

<template>
  <aside v-if="task" class="task-detail" aria-labelledby="task-detail-title">
    <header class="task-detail__header">
      <h2 id="task-detail-title">{{ task.title }}</h2>
      <BaseButton variant="ghost" aria-label="Close" @click="emit('close')">Close</BaseButton>
    </header>
    <p class="task-detail__due">{{ dueText() }}</p>
    <div class="task-detail__labels">
      <LabelBadge v-for="label in task.labels" :key="label.id" :label="label" removable @remove="removeLabel" />
    </div>
    <section class="task-detail__description">{{ task.description }}</section>
    <CommentList :task-id="task.id" />
    <footer v-if="board.canEdit.value" class="task-detail__footer">
      <BaseButton variant="danger" @click="deleteCurrent">Delete task</BaseButton>
    </footer>
  </aside>
</template>
