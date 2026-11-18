<template>
  <form class="reply-form" v-on:submit.prevent="submitReply">
    <h3 id="reply-form-title">Reply</h3>
    <canned-response-picker v-if="showCanned" @insert="insertCanned" @close="showCanned = false" />
    <textarea
      id="reply-body"
      v-model="body"
      rows="6"
      aria-labelledby="reply-form-title"
      :class="{ 'has-error': errors.body }"
      @keydown.ctrl.enter="submitReply"
    ></textarea>
    <p v-if="errors.body" class="field-error">{{ errors.body }}</p>
    <label class="reply-form__internal">
      <input v-model="internal" type="checkbox"> Internal note
    </label>
    <div class="reply-form__actions">
      <base-button variant="secondary" @click="toggleCanned">Canned responses</base-button>
      <base-button type="submit" :busy="sending">Send reply</base-button>
    </div>
  </form>
</template>

<script>
import CannedResponsePicker from './CannedResponsePicker.vue'
import { formValidationMixin } from '@/mixins/formValidationMixin'
import { EventBus } from '../../eventBus'

const MAX_REPLY_LENGTH = 10000

export default {
  name: 'TicketReplyForm',
  components: {
    CannedResponsePicker
  },
  mixins: [formValidationMixin],
  props: {
    ticketId: {
      type: [Number, String],
      required: true
    }
  },
  data() {
    return {
      body: '',
      internal: false,
      sending: false,
      showCanned: false
    }
  },
  methods: {
    validate() {
      this.clearErrors()
      return this.validateRequired('body', this.body, 'Reply') &&
        this.validateMaxLength('body', this.body, MAX_REPLY_LENGTH)
    },
    toggleCanned() {
      this.showCanned = !this.showCanned
    },
    insertCanned(text) {
      this.body = this.body ? this.body + '\n\n' + text : text
      this.showCanned = false
    },
    async submitReply() {
      if (!this.validate()) {
        return
      }
      this.sending = true
      try {
        const reply = await this.$store.dispatch('tickets/postReply', {
          ticketId: this.ticketId,
          body: this.body,
          internal: this.internal
        })
        EventBus.$emit('reply:posted', { ticketId: this.ticketId, reply })
        this.$emit('reply-sent', reply)
        this.body = ''
      } finally {
        this.sending = false
      }
    }
  }
}
</script>
