<template>
  <div class="tickets-view">
    <header class="page-header">
      <h1>Tickets</h1>
      <base-button @click="showNew = true">New ticket</base-button>
    </header>
    <ticket-list :selected-id="selectedId" />
    <base-modal :open="showNew" title="New ticket" @close="showNew = false">
      <new-ticket-form @created="onCreated" @cancel="showNew = false" />
    </base-modal>
  </div>
</template>

<script>
import { mapMutations } from 'vuex'
import TicketList from '@/components/tickets/TicketList.vue'
import NewTicketForm from '@/components/tickets/NewTicketForm.vue'

export default {
  name: 'Tickets',
  components: {
    TicketList,
    NewTicketForm
  },
  data() {
    return {
      showNew: false,
      selectedId: null
    }
  },
  watch: {
    '$route.query.q': {
      handler: 'syncQuery',
      immediate: true
    }
  },
  methods: {
    ...mapMutations('tickets', ['SET_FILTERS']),
    syncQuery(query) {
      this.SET_FILTERS({ query: query || '' })
    },
    onCreated(ticket) {
      this.showNew = false
      this.selectedId = ticket.id
      this.$store.dispatch('tickets/fetchTickets', { page: 1 })
    }
  }
}
</script>
