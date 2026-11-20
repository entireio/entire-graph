import { computed, ref, type Ref } from 'vue'
import type { Priority, Task } from '@/types/task'

export interface TaskFilterState {
  query: string
  priorities: Priority[]
  assigneeId: string | null
  labelIds: string[]
}

export function emptyFilters(): TaskFilterState {
  return { query: '', priorities: [], assigneeId: null, labelIds: [] }
}

export function matchesFilters(task: Task, filters: TaskFilterState): boolean {
  const query = filters.query.toLowerCase()
  if (query && !task.title.toLowerCase().includes(query) && !task.description.toLowerCase().includes(query)) {
    return false
  }
  if (filters.priorities.length > 0 && !filters.priorities.includes(task.priority)) {
    return false
  }
  if (filters.assigneeId !== null && task.assignee?.id !== filters.assigneeId) {
    return false
  }
  return filters.labelIds.every((labelId) => task.labels.some((label) => label.id === labelId))
}

export function useFilters(tasks: Ref<Task[]>) {
  const filters = ref<TaskFilterState>(emptyFilters())

  const filteredTasks = computed(() => tasks.value.filter((task) => matchesFilters(task, filters.value)))
  const activeCount = computed(
    () => filters.value.priorities.length + filters.value.labelIds.length + (filters.value.assigneeId ? 1 : 0),
  )

  function setQuery(query: string) {
    filters.value.query = query.trim()
  }

  function togglePriority(priority: Priority) {
    const current = filters.value.priorities
    filters.value.priorities = current.includes(priority)
      ? current.filter((item) => item !== priority)
      : [...current, priority]
  }

  function setAssignee(assigneeId: string | null) {
    filters.value.assigneeId = assigneeId
  }

  function clearFilters() {
    filters.value = emptyFilters()
  }

  return { filters, filteredTasks, activeCount, setQuery, togglePriority, setAssignee, clearFilters }
}
