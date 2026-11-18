<template>
  <div class="stat-card" :class="toneClass">
    <span class="stat-card__label">{{ label }}</span>
    <strong class="stat-card__value">{{ value }}</strong>
  </div>
</template>

<script>
export default {
  name: 'StatCard',
  props: {
    label: {
      type: String,
      required: true
    },
    value: {
      type: [Number, String],
      default: 0
    },
    tone: {
      type: String,
      default: 'neutral',
      validator: (value) => ['neutral', 'success', 'warning', 'danger'].indexOf(value) !== -1
    }
  },
  computed: {
    toneClass() {
      return 'stat-card--' + this.tone
    }
  }
}
</script>
