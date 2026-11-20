import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useTaskStore } from '@/stores/tasks'
import { createTask, postTaskMove } from '@/api/tasks'
import { makeTask } from '../fixtures'

vi.mock('@/api/tasks', () => ({
  fetchTasks: vi.fn(async () => []),
  createTask: vi.fn(),
  updateTask: vi.fn(),
  deleteTask: vi.fn(),
  postTaskMove: vi.fn(),
}))

describe('useTaskStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('adds a created task', async () => {
    vi.mocked(createTask).mockResolvedValue(makeTask({ id: 't9' }))
    const store = useTaskStore()
    await store.addTask('p1', { title: 'New', description: '', priority: 'low', assigneeId: null, labelIds: [] })
    expect(store.tasks.map((task) => task.id)).toEqual(['t9'])
  })

  it('rolls back a failed move', async () => {
    vi.mocked(postTaskMove).mockRejectedValue(new Error('offline'))
    const store = useTaskStore()
    store.tasks = [makeTask({ id: 't1', columnId: 'c1', position: 0 })]
    await store.moveTask({ taskId: 't1', fromColumnId: 'c1', toColumnId: 'c2', position: 4 })
    expect(store.findTask('t1')?.columnId).toBe('c1')
  })
})
