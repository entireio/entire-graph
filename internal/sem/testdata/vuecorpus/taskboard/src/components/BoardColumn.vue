<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Column } from '@/types/project'
import type { Task, TaskMovedPayload } from '@/types/task'
import { useDragDrop } from '../composables/useDragDrop'
import { pluralize } from '@/utils/format'
import TaskCard from './TaskCard.vue'

const props = defineProps<{
  column: Column
  tasks: Task[]
  selectedTaskId?: string | null
}>()

const emit = defineEmits<{
  'task-moved': [payload: TaskMovedPayload]
  'task-selected': [task: Task]
  'task-toggled': [taskId: string, done: boolean]
}>()

const { overColumnId, startDrag, dragOver, drop, endDrag } = useDragDrop((payload) => emit('task-moved', payload))

const collapsed = ref(false)
const isOver = computed(() => overColumnId.value === props.column.id)
const overLimit = computed(() => props.column.wipLimit !== null && props.tasks.length > props.column.wipLimit)

function countLabel(): string {
  return pluralize(props.tasks.length, 'task')
}

function onDragStart(event: DragEvent, taskId: string) {
  startDrag(event, taskId, props.column.id)
}

function onDrop() {
  drop(props.column.id, props.tasks.length)
}

function onSelect(task: Task) {
  emit('task-selected', task)
}

function onToggleDone(taskId: string, done: boolean) {
  emit('task-toggled', taskId, done)
}

function toggleCollapsed() {
  collapsed.value = !collapsed.value
}
</script>

<template>
  <section
    class="board-column"
    :class="{ 'is-over': isOver, 'is-collapsed': collapsed, 'over-limit': overLimit }"
    :data-column-id="column.id"
    @dragover.prevent="dragOver(column.id)"
    @drop.prevent="onDrop"
  >
    <header class="board-column__header">
      <button type="button" class="board-column__toggle" @click="toggleCollapsed">
        <slot name="title" :column="column" :count="tasks.length">
          <h2>{{ column.title }}</h2>
        </slot>
      </button>
      <span class="board-column__count">{{ countLabel() }}</span>
    </header>
    <TransitionGroup v-show="!collapsed" tag="ol" name="card" class="board-column__list">
      <li v-for="task in tasks" :key="task.id">
        <TaskCard
          :task="task"
          :selected="task.id === selectedTaskId"
          @select="onSelect"
          @toggle-done="onToggleDone"
          @drag-start="onDragStart"
          @dragend="endDrag"
        />
      </li>
    </TransitionGroup>
    <slot name="footer" :column="column" />
  </section>
</template>
