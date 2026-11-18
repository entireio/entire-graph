<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { fetchComments, postComment } from '@/api/comments'
import { usePagination } from '@/composables/usePagination'
import { useAuthStore } from '@/stores/auth'
import type { Comment } from '@/types/task'
import CommentItem from './CommentItem.vue'
import PaginationBar from './PaginationBar.vue'

const props = defineProps<{ taskId: string }>()

const auth = useAuthStore()
const comments = ref<Comment[]>([])
const draft = ref('')
const posting = ref(false)
const pageSize = ref(5)
const commentInputId = `comment-input-${props.taskId}`
const { pageItems, page, pageCount, next, prev } = usePagination(comments, pageSize)

async function load() {
  comments.value = await fetchComments(props.taskId)
}

async function submitComment() {
  const body = draft.value.trim()
  if (!body) {
    return
  }
  posting.value = true
  try {
    const comment = await postComment(props.taskId, body)
    comments.value = [comment, ...comments.value]
    draft.value = ''
  } finally {
    posting.value = false
  }
}

function startReply(comment: Comment) {
  draft.value = `@${comment.author.name} `
}

onMounted(load)
watch(() => props.taskId, load)
</script>

<template>
  <section class="comment-list" aria-label="Comments">
    <ol>
      <CommentItem
        v-for="comment in pageItems"
        :key="comment.id"
        :comment="comment"
        :own="comment.author.id === auth.currentUser?.id"
        @reply="startReply"
      >
        <template #actions="{ comment: item }">
          <button type="button" class="comment__reply" @click="startReply(item)">Reply</button>
        </template>
      </CommentItem>
    </ol>
    <PaginationBar v-if="pageCount > 1" :page="page" :page-count="pageCount" @next="next" @prev="prev" />
    <form v-if="auth.isAuthenticated" class="comment-list__form" @submit.prevent="submitComment">
      <label :for="commentInputId" class="visually-hidden">Comment</label>
      <textarea :id="commentInputId" v-model="draft" rows="3" placeholder="Add a comment" />
      <BaseButton type="submit" variant="primary" :loading="posting">Comment</BaseButton>
    </form>
  </section>
</template>
