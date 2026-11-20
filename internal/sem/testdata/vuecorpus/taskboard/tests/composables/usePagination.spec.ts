import { describe, expect, it } from 'vitest'
import { nextTick, ref } from 'vue'
import { clampPage, usePagination } from '@/composables/usePagination'

describe('usePagination', () => {
  it('slices items into pages', () => {
    const items = ref([1, 2, 3, 4, 5])
    const { pageItems, pageCount, next } = usePagination(items, ref(2))
    expect(pageCount.value).toBe(3)
    next()
    expect(pageItems.value).toEqual([3, 4])
  })

  it('clamps the page when items shrink', async () => {
    const items = ref([1, 2, 3, 4, 5])
    const { page, goTo } = usePagination(items, ref(2))
    goTo(3)
    items.value = [1]
    await nextTick()
    expect(page.value).toBe(1)
  })
})

describe('clampPage', () => {
  it('keeps the page within bounds', () => {
    expect(clampPage(0, 4)).toBe(1)
    expect(clampPage(9, 4)).toBe(4)
  })
})
