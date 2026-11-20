<template lang="pug">
.canned-picker
  input.canned-picker__search(v-model="search" placeholder="Filter responses" @keyup.esc="close")
  ul.canned-picker__list
    li.canned-picker__item(
      v-for="response in filtered"
      :key="response.id"
      @click="insert(response)"
    )
      strong {{ response.title }}
      span.canned-picker__preview {{ response.body | truncate(60) }}
  p.canned-picker__empty(v-if="!filtered.length") No canned responses match.
  base-button(variant="link" @click="close") Close
</template>

<script>
import { mapState, mapActions } from 'vuex'

export default {
  name: 'CannedResponsePicker',
  data() {
    return {
      search: ''
    }
  },
  computed: {
    ...mapState('agents', ['cannedResponses']),
    filtered() {
      const term = this.search.toLowerCase()
      return this.cannedResponses.filter((response) => response.title.toLowerCase().indexOf(term) !== -1)
    }
  },
  created() {
    if (!this.cannedResponses.length) {
      this.loadCannedResponses()
    }
  },
  methods: {
    ...mapActions('agents', ['loadCannedResponses']),
    insert(response) {
      this.$emit('insert', response.body)
    },
    close() {
      this.$emit('close')
    }
  }
}
</script>
