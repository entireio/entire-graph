<template>
  <span class="spinner" :class="'spinner--' + size" role="status">
    <span class="sr-only">{{ label }}</span>
  </span>
</template>

<script>
export default {
  name: 'BaseSpinner',
  props: {
    size: {
      type: String,
      default: 'medium',
      validator: (value) => ['small', 'medium', 'large'].indexOf(value) !== -1
    },
    label: {
      type: String,
      default: 'Loading'
    }
  }
}
</script>

<style scoped>
.spinner {
  display: inline-block;
  border: 2px solid currentColor;
  border-right-color: transparent;
  border-radius: 50%;
  animation: spin 0.75s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
