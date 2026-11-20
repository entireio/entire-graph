<template>
  <button
    :type="type"
    :class="['btn', 'btn--' + variant, { 'btn--busy': busy }]"
    :disabled="disabled || busy"
    @click="handleClick"
  >
    <base-spinner v-if="busy" size="small" />
    <slot />
  </button>
</template>

<script>
export default {
  name: 'BaseButton',
  props: {
    variant: {
      type: String,
      default: 'primary',
      validator: (value) => ['primary', 'secondary', 'danger', 'link'].indexOf(value) !== -1
    },
    type: {
      type: String,
      default: 'button'
    },
    disabled: {
      type: Boolean,
      default: false
    },
    busy: {
      type: Boolean,
      default: false
    }
  },
  methods: {
    handleClick(event) {
      if (!this.disabled && !this.busy) {
        this.$emit('click', event)
      }
    }
  }
}
</script>

<style scoped>
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 14px;
  border-radius: 4px;
}
.btn--link {
  background: none;
  border: 0;
}
</style>
