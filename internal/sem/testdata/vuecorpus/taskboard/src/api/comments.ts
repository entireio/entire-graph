import { request } from './client'
import type { Comment } from '@/types/task'

export function fetchComments(taskId: string, page = 1): Promise<Comment[]> {
  return request<Comment[]>('GET', `/tasks/${taskId}/comments`, undefined, { page })
}

export function postComment(taskId: string, body: string): Promise<Comment> {
  return request<Comment>('POST', `/tasks/${taskId}/comments`, { body })
}
