import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { createTask, deleteTask, fetchTasks, postTaskMove, updateTask } from '@/api/tasks'
import type { Task, TaskDraft, TaskMovedPayload } from '@/types/task'
import { bus } from '@/utils/eventBus'

export const useTaskStore = defineStore('tasks', () => {
  const tasks = ref<Task[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  const tasksByColumn = computed(() => {
    const groups: Record<string, Task[]> = {}
    for (const task of tasks.value) {
      ;(groups[task.columnId] ??= []).push(task)
    }
    for (const list of Object.values(groups)) {
      list.sort((a, b) => a.position - b.position)
    }
    return groups
  })

  function findTask(taskId: string): Task | undefined {
    return tasks.value.find((task) => task.id === taskId)
  }

  async function loadTasks(projectId: string) {
    loading.value = true
    error.value = null
    try {
      tasks.value = await fetchTasks(projectId)
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err)
    } finally {
      loading.value = false
    }
  }

  async function addTask(projectId: string, draft: TaskDraft) {
    const task = await createTask(projectId, draft)
    tasks.value.push(task)
    bus.emit('task:created', task)
    return task
  }

  async function editTask(taskId: string, changes: Partial<Task>) {
    const updated = await updateTask(taskId, changes)
    const index = tasks.value.findIndex((task) => task.id === taskId)
    if (index !== -1) {
      tasks.value.splice(index, 1, updated)
    }
    return updated
  }

  async function removeTask(taskId: string) {
    await deleteTask(taskId)
    tasks.value = tasks.value.filter((task) => task.id !== taskId)
    bus.emit('task:deleted', taskId)
  }

  async function moveTask(payload: TaskMovedPayload) {
    const task = findTask(payload.taskId)
    if (!task) {
      return
    }
    const previous = { columnId: task.columnId, position: task.position }
    task.columnId = payload.toColumnId
    task.position = payload.position
    try {
      await postTaskMove(payload)
    } catch {
      Object.assign(task, previous)
    }
  }

  return { tasks, loading, error, tasksByColumn, findTask, loadTasks, addTask, editTask, removeTask, moveTask }
})
