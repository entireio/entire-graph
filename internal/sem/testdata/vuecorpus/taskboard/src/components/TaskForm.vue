<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import type { Priority, TaskDraft } from '@/types/task'
import type { User } from '@/types/user'
import AssigneePicker from './AssigneePicker.vue'

const props = withDefaults(defineProps<{ members: User[]; initial?: Partial<TaskDraft> }>(), {
  initial: () => ({}),
})

const emit = defineEmits<{
  submit: [draft: TaskDraft]
  cancel: []
}>()

const priorities: Priority[] = ['low', 'medium', 'high', 'urgent']
const titleInput = ref<HTMLInputElement | null>(null)
const draft = ref<TaskDraft>(emptyDraft())
const canSubmit = computed(() => draft.value.title.trim().length > 0)

function emptyDraft(): TaskDraft {
  return { title: '', description: '', priority: 'medium', assigneeId: null, labelIds: [], ...props.initial }
}

function onSubmit() {
  if (!canSubmit.value) {
    return
  }
  emit('submit', { ...draft.value, title: draft.value.title.trim() })
}

function reset() {
  draft.value = emptyDraft()
}

async function focusTitle() {
  await nextTick()
  titleInput.value?.focus()
}

defineExpose({ focusTitle, reset })
</script>

<template>
  <form class="task-form" @submit.prevent="onSubmit">
    <label for="task-title">Title</label>
    <input id="task-title" ref="titleInput" v-model="draft.title" type="text" required aria-describedby="task-title-hint" />
    <small id="task-title-hint">Keep it short and specific.</small>
    <label for="task-description">Description</label>
    <textarea id="task-description" v-model="draft.description" rows="4" />
    <fieldset class="task-form__priority">
      <legend>Priority</legend>
      <label v-for="priority in priorities" :key="priority">
        <input v-model="draft.priority" type="radio" name="priority" :value="priority" />
        {{ priority }}
      </label>
    </fieldset>
    <AssigneePicker v-model:assignee-id="draft.assigneeId" :members="members" />
    <div class="task-form__actions">
      <base-button type="button" @click="emit('cancel')">Cancel</base-button>
      <base-button type="submit" variant="primary" :disabled="!canSubmit">Create task</base-button>
    </div>
  </form>
</template>
