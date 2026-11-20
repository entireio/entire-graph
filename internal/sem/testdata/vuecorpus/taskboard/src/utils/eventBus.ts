import type { Task } from '@/types/task'

type Handler<T> = (payload: T) => void

export interface BoardEvents {
  'task:created': Task
  'task:deleted': string
  'session:expired': undefined
}

export class EventBus<Events extends Record<string, unknown>> {
  private handlers = new Map<keyof Events, Set<Handler<never>>>()

  on<K extends keyof Events>(event: K, handler: Handler<Events[K]>): () => void {
    const set = this.handlers.get(event) ?? new Set()
    set.add(handler as Handler<never>)
    this.handlers.set(event, set)
    return () => this.off(event, handler)
  }

  off<K extends keyof Events>(event: K, handler: Handler<Events[K]>): void {
    this.handlers.get(event)?.delete(handler as Handler<never>)
  }

  emit<K extends keyof Events>(event: K, payload: Events[K]): void {
    for (const handler of this.handlers.get(event) ?? []) {
      ;(handler as Handler<Events[K]>)(payload)
    }
  }
}

export const bus = new EventBus<BoardEvents>()
