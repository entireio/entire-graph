import type { User } from './user'

export interface Column {
  id: string
  title: string
  wipLimit: number | null
  taskIds: string[]
}

export interface Board {
  id: string
  projectId: string
  columns: Column[]
}

export interface Project {
  id: string
  key: string
  name: string
  description: string
  members: User[]
  boardId: string
  archived: boolean
}
