<template>
  <base-list />
</template>

<script>
import BaseList from './BaseList'

export default {
  extends: BaseList,
  methods: {
    fetchPage(page) {
      return this.nextPage(page)
    }
  }
}
</script>
