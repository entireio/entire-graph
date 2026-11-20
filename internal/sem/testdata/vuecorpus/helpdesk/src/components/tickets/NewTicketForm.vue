<template>
  <form id="new-ticket-form" class="new-ticket-form" @submit.prevent="submit">
    <label for="new-ticket-subject">Subject</label>
    <input id="new-ticket-subject" v-model.trim="form.subject" type="text">
    <p v-if="errors.subject" class="field-error">{{ errors.subject }}</p>
    <label for="new-ticket-customer">Customer</label>
    <select id="new-ticket-customer" v-model="form.customerId">
      <option v-for="option in customerOptions" :key="option.value" :value="option.value">
        {{ option.label }}
      </option>
    </select>
    <p v-if="errors.customerId" class="field-error">{{ errors.customerId }}</p>
    <label for="new-ticket-priority">Priority</label>
    <select id="new-ticket-priority" v-model="form.priority">
      <option v-for="priority in priorities" :key="priority" :value="priority">
        {{ priority | priority }}
      </option>
    </select>
    <textarea v-model="form.description" rows="5" aria-label="Description"></textarea>
    <div class="form-actions">
      <base-button variant="link" @click="cancel">Cancel</base-button>
      <base-button type="submit" :busy="saving">Create</base-button>
    </div>
  </form>
</template>

<script>
import { mapGetters } from 'vuex'
import { createTicket } from '@/api/tickets'
import { PRIORITIES } from '@/utils/priority'
import { formValidationMixin } from '@/mixins/formValidationMixin'

export default {
  name: 'NewTicketForm',
  mixins: [formValidationMixin],
  data() {
    return {
      saving: false,
      priorities: PRIORITIES,
      form: {
        subject: '',
        customerId: '',
        priority: 'normal',
        description: ''
      }
    }
  },
  computed: {
    ...mapGetters('customers', ['customerOptions'])
  },
  created() {
    if (!this.customerOptions.length) {
      this.$store.dispatch('customers/fetchCustomers', { pageSize: 100 })
    }
  },
  methods: {
    validate() {
      this.clearErrors()
      const subjectOk = this.validateRequired('subject', this.form.subject, 'Subject')
      const customerOk = this.validateRequired('customerId', this.form.customerId, 'Customer')
      return subjectOk && customerOk
    },
    async submit() {
      if (!this.validate()) {
        return
      }
      this.saving = true
      try {
        const ticket = await createTicket(this.form)
        this.$emit('created', ticket)
      } finally {
        this.saving = false
      }
    },
    cancel() {
      this.clearErrors()
      this.$emit('cancel')
    }
  }
}
</script>
