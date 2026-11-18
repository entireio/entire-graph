// Shared paging state for list views. A component that uses this mixin
// implements fetchPage() and calls goToPage() when its filters change.
export const paginationMixin = {
  props: {
    initialPageSize: {
      type: Number,
      default: 25,
      validator: (value) => [10, 25, 50, 100].indexOf(value) !== -1
    }
  },
  data() {
    return {
      page: 1,
      pageSize: this.initialPageSize,
      total: 0
    }
  },
  computed: {
    totalPages() {
      return Math.max(1, Math.ceil(this.total / this.pageSize))
    },
    hasNextPage() {
      return this.page < this.totalPages
    },
    hasPrevPage() {
      return this.page > 1
    }
  },
  watch: {
    pageSize() {
      this.goToPage(1)
    }
  },
  methods: {
    goToPage(page) {
      this.page = Math.min(Math.max(1, page), this.totalPages)
      return this.fetchPage()
    },
    nextPage() {
      if (this.hasNextPage) {
        return this.goToPage(this.page + 1)
      }
      return Promise.resolve()
    },
    prevPage() {
      if (this.hasPrevPage) {
        return this.goToPage(this.page - 1)
      }
      return Promise.resolve()
    },
    setTotal(total) {
      this.total = total
    }
  }
}

export default paginationMixin
