export const paging = {
  methods: {
    nextPage(page) {
      return page + 1
    },
    refresh() {
      return this.$store.dispatch('counter/save', 0)
    }
  }
}
