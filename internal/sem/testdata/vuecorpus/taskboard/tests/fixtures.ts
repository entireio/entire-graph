import type { Task } from '@/types/task'
import type { User } from '@/types/user'

export function makeUser(overrides: Partial<User> = {}): User {
  return {
    id: 'u1',
    name: 'Ada Lovelace',
    email: 'ada@example.com',
    avatarUrl: null,
    role: 'member',
    ...overrides,
  }
}

export function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: 't1',
    projectId: 'p1',
    columnId: 'c1',
    title: 'Write release notes',
    description: '',
    status: 'todo',
    priority: 'medium',
    assignee: null,
    labels: [],
    dueDate: null,
    position: 0,
    ...overrides,
  }
}
