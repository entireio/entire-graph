import { request } from './client'
import type { Task, TaskDraft, TaskMovedPayload } from '@/types/task'

export function fetchTasks(projectId: string): Promise<Task[]> {
  return request<Task[]>('GET', `/projects/${projectId}/tasks`)
}

export function createTask(projectId: string, draft: TaskDraft): Promise<Task> {
  return request<Task>('POST', `/projects/${projectId}/tasks`, draft)
}

export function updateTask(taskId: string, changes: Partial<Task>): Promise<Task> {
  return request<Task>('PATCH', `/tasks/${taskId}`, changes)
}

export function deleteTask(taskId: string): Promise<void> {
  return request<void>('DELETE', `/tasks/${taskId}`)
}

export function postTaskMove(payload: TaskMovedPayload): Promise<Task> {
  return request<Task>('POST', `/tasks/${payload.taskId}/move`, payload)
}
