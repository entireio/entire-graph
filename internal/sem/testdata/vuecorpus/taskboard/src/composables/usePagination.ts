import { computed, ref, watch, type Ref } from 'vue'

export function clampPage(target: number, pageCount: number): number {
  return Math.min(Math.max(1, target), Math.max(1, pageCount))
}

export function usePagination<T>(items: Ref<T[]>, pageSize: Ref<number>) {
  const page = ref(1)
  const pageCount = computed(() => Math.max(1, Math.ceil(items.value.length / pageSize.value)))
  const pageItems = computed(() => {
    const start = (page.value - 1) * pageSize.value
    return items.value.slice(start, start + pageSize.value)
  })

  function goTo(target: number) {
    page.value = clampPage(target, pageCount.value)
  }

  function next() {
    goTo(page.value + 1)
  }

  function prev() {
    goTo(page.value - 1)
  }

  watch(pageCount, (count) => {
    if (page.value > count) {
      goTo(count)
    }
  })

  return { page, pageCount, pageItems, goTo, next, prev }
}
