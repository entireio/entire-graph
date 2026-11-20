import { request } from './client'
import type { Board, Project } from '@/types/project'
import type { User } from '@/types/user'

export function fetchProjects(): Promise<Project[]> {
  return request<Project[]>('GET', '/projects')
}

export function fetchBoard(boardId: string): Promise<Board> {
  return request<Board>('GET', `/boards/${boardId}`)
}

export function fetchMembers(projectId: string): Promise<User[]> {
  return request<User[]>('GET', `/projects/${projectId}/members`)
}
