import { onBeforeUnmount, ref } from 'vue'
import type { Task } from '@/types/task'
import { bus } from '@/utils/eventBus'

export interface Notice {
  id: number
  message: string
  tone: 'info' | 'success' | 'error'
}

let nextNoticeId = 1

export function useNotifications() {
  const notices = ref<Notice[]>([])

  function notify(message: string, tone: Notice['tone'] = 'info') {
    const notice: Notice = { id: nextNoticeId++, message, tone }
    notices.value.push(notice)
    window.setTimeout(() => dismiss(notice.id), 4000)
  }

  function dismiss(noticeId: number) {
    notices.value = notices.value.filter((notice) => notice.id !== noticeId)
  }

  function onTaskCreated(task: Task) {
    notify(`Created "${task.title}"`, 'success')
  }

  const stopCreated = bus.on('task:created', onTaskCreated)
  const stopDeleted = bus.on('task:deleted', () => notify('Task deleted'))
  onBeforeUnmount(() => {
    stopCreated()
    stopDeleted()
  })

  return { notices, notify, dismiss }
}
