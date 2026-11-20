import type { InjectionKey, Ref } from 'vue'
import type { Board } from '@/types/project'
import type { Task } from '@/types/task'
import type { Theme } from './stores/settings'

export interface BoardContext {
  board: Ref<Board | null>
  canEdit: Ref<boolean>
  openTask: (task: Task) => void
}

export const BoardContextKey: InjectionKey<BoardContext> = Symbol('BoardContext')

export const ThemeKey: InjectionKey<Ref<Theme>> = Symbol('Theme')
