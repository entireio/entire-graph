<script setup lang="ts">
import { computed } from 'vue'
import type { User } from '@/types/user'
import AssigneeAvatar from './AssigneeAvatar.vue'

const props = defineProps<{ members: User[] }>()

const assigneeId = defineModel<string | null>('assigneeId', { default: null })

const selected = computed(() => props.members.find((member) => member.id === assigneeId.value) ?? null)

function choose(member: User | null) {
  assigneeId.value = member ? member.id : null
}
</script>

<template>
  <div class="assignee-picker" role="listbox" aria-label="Assignee">
    <button type="button" class="assignee-picker__none" :class="{ active: !selected }" @click="choose(null)">
      Unassigned
    </button>
    <button
      v-for="member in members"
      :key="member.id"
      type="button"
      :class="{ active: selected?.id === member.id }"
      @click="choose(member)"
    >
      <AssigneeAvatar :user="member" size="sm" />
      {{ member.name }}
    </button>
  </div>
</template>
