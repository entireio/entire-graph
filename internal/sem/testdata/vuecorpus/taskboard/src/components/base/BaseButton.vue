<script setup lang="ts">
import { computed } from 'vue'

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger'

const props = withDefaults(
  defineProps<{
    variant?: Variant
    type?: 'button' | 'submit' | 'reset'
    loading?: boolean
    disabled?: boolean
  }>(),
  { variant: 'secondary', type: 'button', loading: false, disabled: false },
)

const emit = defineEmits<{
  click: [event: MouseEvent]
}>()

const classes = computed(() => ['btn', `btn-${props.variant}`, { 'btn-loading': props.loading }])

function handleClick(event: MouseEvent) {
  if (props.loading || props.disabled) {
    return
  }
  emit('click', event)
}
</script>

<template>
  <button :type="type" :class="classes" :disabled="disabled || loading" @click="handleClick">
    <span v-if="loading" class="btn-spinner" aria-hidden="true" />
    <slot />
  </button>
</template>

<style scoped>
.btn {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
}
</style>
