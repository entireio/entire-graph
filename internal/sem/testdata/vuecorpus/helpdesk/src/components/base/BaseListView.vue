<template>
  <section class="list-view">
    <h1 class="list-view__title">{{ title }}</h1>
    <base-spinner v-if="loading" />
    <p v-else-if="!rows.length" class="list-view__empty">{{ emptyText }}</p>
    <slot v-else :rows="rows" />
  </section>
</template>

<script>
import { paginationMixin } from '@/mixins/paginationMixin'

// Base for paged list views. Views extend it, override fetchPage(), and wrap
// their loader in load() so rows, totals and the loading flag stay in sync.
export default {
  name: 'BaseListView',
  mixins: [paginationMixin],
  props: {
    title: {
      type: String,
      default: ''
    },
    emptyText: {
      type: String,
      default: 'Nothing to show'
    }
  },
  data() {
    return {
      rows: [],
      loading: false,
      loadError: null
    }
  },
  mounted() {
    this.goToPage(1)
  },
  methods: {
    fetchPage() {
      return Promise.resolve()
    },
    async load(loader) {
      this.loading = true
      this.loadError = null
      try {
        const result = await loader()
        this.rows = result.items
        this.setTotal(result.total)
        return result
      } catch (error) {
        this.loadError = error
        throw error
      } finally {
        this.loading = false
      }
    }
  }
}
</script>
