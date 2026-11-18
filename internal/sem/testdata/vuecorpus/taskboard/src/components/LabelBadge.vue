<script lang="tsx">
import { defineComponent, type PropType } from 'vue'
import type { Label } from '@/types/task'

export function contrastColor(hex: string): string {
  const value = hex.replace('#', '')
  const r = parseInt(value.slice(0, 2), 16)
  const g = parseInt(value.slice(2, 4), 16)
  const b = parseInt(value.slice(4, 6), 16)
  return (r * 299 + g * 587 + b * 114) / 1000 > 140 ? '#1f2328' : '#ffffff'
}

function badgeStyle(label: Label) {
  return { backgroundColor: label.color, color: contrastColor(label.color) }
}

export default defineComponent({
  name: 'LabelBadge',
  props: {
    label: { type: Object as PropType<Label>, required: true },
    removable: { type: Boolean, default: false },
  },
  emits: ['remove'],
  setup(props, { emit }) {
    const onRemove = () => emit('remove', props.label.id)
    return () => (
      <span class="label-badge" style={badgeStyle(props.label)}>
        {props.label.name}
        {props.removable && (
          <button type="button" class="label-badge__remove" aria-label={`Remove ${props.label.name}`} onClick={onRemove}>
            x
          </button>
        )}
      </span>
    )
  },
})
</script>

<style scoped>
.label-badge {
  border-radius: 999px;
  padding: 0 0.5rem;
}
</style>
