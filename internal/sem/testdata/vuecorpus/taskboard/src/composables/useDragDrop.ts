import { ref } from 'vue'
import type { TaskMovedPayload } from '@/types/task'

export function useDragDrop(onMove: (payload: TaskMovedPayload) => void) {
  const draggingTaskId = ref<string | null>(null)
  const sourceColumnId = ref<string | null>(null)
  const overColumnId = ref<string | null>(null)

  function startDrag(event: DragEvent, taskId: string, columnId: string) {
    draggingTaskId.value = taskId
    sourceColumnId.value = columnId
    event.dataTransfer?.setData('text/plain', taskId)
  }

  function dragOver(columnId: string) {
    overColumnId.value = columnId
  }

  function drop(columnId: string, position: number) {
    if (draggingTaskId.value && sourceColumnId.value) {
      onMove({ taskId: draggingTaskId.value, fromColumnId: sourceColumnId.value, toColumnId: columnId, position })
    }
    endDrag()
  }

  function endDrag() {
    draggingTaskId.value = null
    sourceColumnId.value = null
    overColumnId.value = null
  }

  return { draggingTaskId, overColumnId, startDrag, dragOver, drop, endDrag }
}
