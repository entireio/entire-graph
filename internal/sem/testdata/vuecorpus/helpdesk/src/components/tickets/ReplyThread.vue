<template>
  <ol class="reply-thread">
    <li v-if="hiddenCount" class="reply-thread__more">
      <base-button variant="link" @click="expand">Show {{ hiddenCount }} earlier replies</base-button>
    </li>
    <li
      v-for="reply in visibleReplies"
      :key="reply.id"
      :class="{ 'reply--internal': reply.internal }"
    >
      <slot name="reply" :reply="reply">
        <p class="reply__meta">{{ reply.authorName }} - {{ reply.createdAt | date(true) }}</p>
        <div class="reply__body">{{ reply.body }}</div>
      </slot>
    </li>
  </ol>
</template>

<script>
export default {
  name: 'ReplyThread',
  props: {
    replies: {
      type: Array,
      default: () => []
    },
    showInternal: {
      type: Boolean,
      default: true
    },
    collapseAfter: {
      type: Number,
      default: 5,
      validator: (value) => value > 0
    }
  },
  data() {
    return {
      expanded: false
    }
  },
  computed: {
    filteredReplies() {
      return this.showInternal ? this.replies : this.replies.filter((reply) => !reply.internal)
    },
    visibleReplies() {
      if (this.expanded || this.filteredReplies.length <= this.collapseAfter) {
        return this.filteredReplies
      }
      return this.filteredReplies.slice(-this.collapseAfter)
    },
    hiddenCount() {
      return this.filteredReplies.length - this.visibleReplies.length
    }
  },
  methods: {
    expand() {
      this.expanded = true
    }
  }
}
</script>
