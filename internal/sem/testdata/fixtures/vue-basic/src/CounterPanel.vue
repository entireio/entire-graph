<template>
  <p>{{ total }}</p>
</template>

<script setup>
import { computed } from 'vue'
import { useStore } from 'vuex'

const store = useStore()
store.dispatch('counter/save', 1)
const total = computed(() => store.getters['counter/total'])
</script>
