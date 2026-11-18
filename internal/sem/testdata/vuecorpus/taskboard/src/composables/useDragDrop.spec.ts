import { describe, expect, it, vi } from 'vitest'
import { useDragDrop } from './useDragDrop'

describe('useDragDrop', () => {
  it('reports a move when a dragged task is dropped on a column', () => {
    const onMove = vi.fn()
    const { startDrag, drop, draggingTaskId } = useDragDrop(onMove)
    startDrag(new DragEvent('dragstart'), 't1', 'c1')
    drop('c2', 3)
    expect(onMove).toHaveBeenCalledWith({ taskId: 't1', fromColumnId: 'c1', toColumnId: 'c2', position: 3 })
    expect(draggingTaskId.value).toBeNull()
  })

  it('ignores a drop with nothing dragged', () => {
    const onMove = vi.fn()
    const { drop } = useDragDrop(onMove)
    drop('c2', 0)
    expect(onMove).not.toHaveBeenCalled()
  })
})
