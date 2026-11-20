<template>
  <section v-if="customer" id="customer-detail" class="customer-detail">
    <customer-card :customer="customer" />
    <form class="customer-form" @submit.prevent="save">
      <label for="customer-name">Name</label>
      <input id="customer-name" v-model="form.name" type="text">
      <p v-if="errors.name" class="field-error">{{ errors.name }}</p>
      <label for="customer-email">Email</label>
      <input id="customer-email" v-model="form.email" type="email">
      <p v-if="errors.email" class="field-error">{{ errors.email }}</p>
      <base-button type="submit" :busy="saving">Save</base-button>
    </form>
    <h2>Tickets</h2>
    <table class="ticket-table">
      <tbody>
        <ticket-row v-for="ticket in tickets" :key="ticket.id" :ticket="ticket" @select="openTicket" />
      </tbody>
    </table>
  </section>
  <base-spinner v-else />
</template>

<script>
import { mapState, mapActions } from 'vuex'
import CustomerCard from '../components/customers/CustomerCard.vue'
import TicketRow from '../components/tickets/TicketRow.vue'
import { formValidationMixin } from '../mixins/formValidationMixin'

export default {
  name: 'CustomerDetail',
  components: {
    CustomerCard,
    TicketRow
  },
  mixins: [formValidationMixin],
  props: {
    id: {
      type: [String, Number],
      required: true
    }
  },
  data() {
    return {
      form: { name: '', email: '' },
      saving: false
    }
  },
  computed: {
    ...mapState('customers', { customer: 'selected', tickets: 'tickets' })
  },
  watch: {
    id: {
      handler() {
        this.loadCustomer()
      },
      immediate: true
    },
    customer(next) {
      if (next) {
        this.form = { name: next.name, email: next.email }
      }
    }
  },
  methods: {
    ...mapActions('customers', ['selectCustomer', 'saveCustomer']),
    loadCustomer() {
      return this.selectCustomer(this.id)
    },
    validate() {
      this.clearErrors()
      const nameOk = this.validateRequired('name', this.form.name, 'Name')
      const emailOk = this.validateEmail('email', this.form.email)
      return nameOk && emailOk
    },
    async save() {
      if (!this.validate()) {
        return
      }
      this.saving = true
      try {
        await this.saveCustomer({ id: this.id, changes: this.form })
        this.$store.commit('notifications/PUSH', { level: 'success', text: 'Customer saved' })
      } finally {
        this.saving = false
      }
    },
    openTicket(ticket) {
      this.$router.push({ name: 'ticket-detail', params: { id: ticket.id } })
    }
  }
}
</script>
