<template>
  <button @click="increment">{{ count }}</button>
</template>

<script>
export default {
  name: 'Counter',
  data() {
    return { count: 0 }
  },
  methods: {
    increment() {
      this.count = this.next(this.count)
      this.$emit('changed', this.count)
    },
    save() {
      return this.$store.dispatch('counter/save', this.count)
    },
    next(value) {
      return value + 1
    }
  }
}
</script>
