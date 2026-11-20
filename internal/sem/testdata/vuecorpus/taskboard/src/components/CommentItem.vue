<script setup lang="ts">
import type { Comment } from '../types/task'
import { formatDate } from '@/utils/format'
import AssigneeAvatar from './AssigneeAvatar.vue'

const props = defineProps<{ comment: Comment; own?: boolean }>()

const emit = defineEmits<{
  (e: 'reply', comment: Comment): void
}>()

function postedAt(): string {
  return formatDate(props.comment.createdAt)
}

function reply() {
  emit('reply', props.comment)
}
</script>

<template>
  <li class="comment" :class="{ 'comment--own': own }">
    <AssigneeAvatar :user="comment.author" size="sm" />
    <div class="comment__body">
      <p class="comment__meta">
        <strong>{{ comment.author.name }}</strong>
        <time :datetime="comment.createdAt">{{ postedAt() }}</time>
      </p>
      <p>{{ comment.body }}</p>
      <slot name="actions" :comment="comment">
        <button type="button" class="comment__reply" @click="reply">Reply</button>
      </slot>
    </div>
  </li>
</template>
