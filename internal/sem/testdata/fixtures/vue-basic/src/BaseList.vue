<template>
  <section><slot /></section>
</template>

<script>
import { paging } from './mixins/paging'

export default {
  mixins: [paging],
  methods: {
    load() {
      return this.fetchPage(this.nextPage(0))
    },
    fetchPage(page) {
      return page
    }
  }
}
</script>
