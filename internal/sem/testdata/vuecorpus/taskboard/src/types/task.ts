import type { User } from './user'

export type TaskStatus = 'todo' | 'in_progress' | 'review' | 'done'

export type Priority = 'low' | 'medium' | 'high' | 'urgent'

export interface Label {
  id: string
  name: string
  color: string
}

export interface Comment {
  id: string
  taskId: string
  author: User
  body: string
  createdAt: string
}

export interface Task {
  id: string
  projectId: string
  columnId: string
  title: string
  description: string
  status: TaskStatus
  priority: Priority
  assignee: User | null
  labels: Label[]
  dueDate: string | null
  position: number
}

export interface TaskDraft {
  title: string
  description: string
  priority: Priority
  assigneeId: string | null
  labelIds: string[]
}

export interface TaskMovedPayload {
  taskId: string
  fromColumnId: string
  toColumnId: string
  position: number
}

export function isOverdue(task: Task, now: Date = new Date()): boolean {
  if (task.dueDate === null || task.status === 'done') {
    return false
  }
  return new Date(task.dueDate).getTime() < now.getTime()
}
