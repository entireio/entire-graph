<script setup lang="ts">
import { computed } from 'vue'
import { isOverdue, type Task } from '@/types/task'
import { formatDate } from '../utils/format'
import LabelBadge from './LabelBadge.vue'
import AssigneeAvatar from './AssigneeAvatar.vue'

interface Props {
  task: Task
  draggable?: boolean
  selected?: boolean
  compact?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  draggable: true,
  selected: false,
  compact: false,
})

const emit = defineEmits<{
  (e: 'select', task: Task): void
  (e: 'toggle-done', taskId: string, done: boolean): void
  (e: 'drag-start', event: DragEvent, taskId: string): void
}>()

const isDone = computed(() => props.task.status === 'done')

function overdue(): boolean {
  return isOverdue(props.task)
}

function dueLabel(): string {
  return formatDate(props.task.dueDate)
}

function select() {
  emit('select', props.task)
}

function toggleDone() {
  emit('toggle-done', props.task.id, !isDone.value)
}

const onDragStart = (event: DragEvent) => {
  emit('drag-start', event, props.task.id)
}
</script>

<template>
  <article
    class="task-card"
    :class="{ 'is-selected': selected, 'is-overdue': overdue(), 'is-done': isDone, compact }"
    :data-task-id="task.id"
    data-id="card"
    :draggable="draggable"
    @click="select"
    @dragstart="onDragStart"
  >
    <header class="task-card__header">
      <input type="checkbox" :checked="isDone" :aria-label="`Mark ${task.title} done`" @click.stop @change="toggleDone" />
      <h3 class="task-card__title">{{ task.title }}</h3>
    </header>
    <ul v-if="task.labels.length" class="task-card__labels">
      <li v-for="label in task.labels" :key="label.id">
        <LabelBadge :label="label" />
      </li>
    </ul>
    <footer class="task-card__footer">
      <span v-if="task.dueDate" class="task-card__due">{{ dueLabel() }}</span>
      <assignee-avatar v-if="task.assignee" :user="task.assignee" size="xs" />
    </footer>
  </article>
</template>
