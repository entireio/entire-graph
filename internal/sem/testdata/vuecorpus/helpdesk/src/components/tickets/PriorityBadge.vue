<template functional>
  <span
    class="priority-badge"
    :class="$options.colorClass(props.priority)"
    :title="'Priority: ' + $options.label(props.priority)"
  >
    {{ $options.label(props.priority) }}
  </span>
</template>

<script>
import { priorityLabel, priorityColor, isPriority } from '@/utils/priority'

// Functional component: the template has no `this`, so helpers live on the
// options object and the template reaches them through $options.
export default {
  name: 'PriorityBadge',
  props: {
    priority: {
      type: String,
      required: true,
      validator: isPriority
    }
  },
  colorClass(priority) {
    return 'priority-badge--' + priorityColor(priority)
  },
  label(priority) {
    return priorityLabel(priority)
  }
}
</script>
