import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import { emptyFilters, matchesFilters, useFilters } from '@/composables/useFilters'
import { makeTask, makeUser } from '../fixtures'

describe('useFilters', () => {
  it('filters tasks by query', () => {
    const tasks = ref([makeTask({ title: 'Write docs' }), makeTask({ id: 't2', title: 'Fix login bug' })])
    const { filteredTasks, setQuery } = useFilters(tasks)
    setQuery('  login ')
    expect(filteredTasks.value.map((task) => task.id)).toEqual(['t2'])
  })

  it('toggles priorities and counts active filters', () => {
    const tasks = ref([makeTask({ priority: 'high' }), makeTask({ id: 't2', priority: 'low' })])
    const { filteredTasks, activeCount, togglePriority, clearFilters } = useFilters(tasks)
    togglePriority('high')
    expect(activeCount.value).toBe(1)
    expect(filteredTasks.value).toHaveLength(1)
    clearFilters()
    expect(activeCount.value).toBe(0)
  })
})

describe('matchesFilters', () => {
  it('matches on assignee', () => {
    const task = makeTask({ assignee: makeUser({ id: 'u7' }) })
    expect(matchesFilters(task, { ...emptyFilters(), assigneeId: 'u7' })).toBe(true)
    expect(matchesFilters(task, { ...emptyFilters(), assigneeId: 'u8' })).toBe(false)
  })
})
