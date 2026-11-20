<template>
  <div id="ticket-detail" class="ticket-detail">
    <base-spinner v-if="!ticket" />
    <template v-else>
      <header class="ticket-detail__header">
        <priority-badge :priority="ticket.priority" />
        <h1>{{ ticket.subject }}</h1>
        <ticket-timer :ticket="ticket" />
        <select v-model="status" class="ticket-detail__status" @change="changeStatus">
          <option v-for="option in statuses" :key="option" :value="option">{{ option }}</option>
        </select>
        <base-button v-if="!ticket.assigneeId" @click="takeOwnership">Take it</base-button>
        <base-button variant="secondary" @click="showAssign = true">Reassign</base-button>
      </header>
      <aside class="ticket-detail__customer">
        <customer-card :customer="ticket.customer" @open="openCustomer" />
      </aside>
      <section id="reply-section">
        <reply-thread :replies="replies" :show-internal="isSupervisor">
          <template slot="reply" slot-scope="{ reply }">
            <p class="reply__meta">{{ reply.authorName }} - {{ reply.createdAt | timeAgo }}</p>
            <div class="reply__body" v-html="reply.bodyHtml"></div>
          </template>
        </reply-thread>
        <ticket-reply-form :ticket-id="ticketKey" @reply-sent="onReplySent" />
      </section>
      <base-modal :open="showAssign" title="Reassign ticket" @close="showAssign = false">
        <agent-select :ticket-id="ticketKey" v-on:assigned="onAssigned" />
        <template v-slot:footer>
          <base-button variant="link" @click="showAssign = false">Cancel</base-button>
        </template>
      </base-modal>
    </template>
  </div>
</template>

<script>
import { mapState, mapGetters } from 'vuex'
import PriorityBadge from '@/components/tickets/PriorityBadge.vue'
import TicketTimer from '@/components/tickets/TicketTimer.vue'
import TicketReplyForm from '@/components/tickets/TicketReplyForm.vue'
import ReplyThread from '@/components/tickets/ReplyThread.vue'
import CustomerCard from '@/components/customers/CustomerCard.vue'
import AgentSelect from '@/components/agents/AgentSelect.vue'
import { slaTimerMixin } from '@/mixins/slaTimerMixin'
import { EventBus } from '@/eventBus'
import { formatDate } from '@/utils/date'

export default {
  name: 'TicketDetail',
  components: {
    PriorityBadge,
    TicketTimer,
    TicketReplyForm,
    ReplyThread,
    CustomerCard,
    AgentSelect
  },
  mixins: [slaTimerMixin],
  props: {
    id: {
      type: [String, Number],
      required: true
    }
  },
  data() {
    return {
      status: '',
      showAssign: false,
      statuses: ['open', 'pending', 'solved', 'closed']
    }
  },
  computed: {
    ...mapState('tickets', { ticket: 'current', replies: 'replies' }),
    ...mapState('auth', ['user']),
    ...mapGetters('auth', ['isSupervisor']),
    ticketKey() {
      return Number(this.id)
    },
    slaTicket() {
      return this.ticket
    }
  },
  watch: {
    id: {
      handler: 'loadTicket',
      immediate: true
    },
    ticket(next) {
      if (next) {
        this.status = next.status
        document.title = next.subject + ' (opened ' + formatDate(next.createdAt) + ')'
      }
    }
  },
  created() {
    EventBus.$on('ticket:updated', this.onTicketUpdated)
  },
  beforeDestroy() {
    EventBus.$off('ticket:updated', this.onTicketUpdated)
  },
  methods: {
    loadTicket() {
      return this.$store.dispatch('tickets/fetchTicket', this.ticketKey)
    },
    async changeStatus() {
      await this.$store.dispatch('tickets/updateStatus', { id: this.ticketKey, status: this.status })
    },
    async takeOwnership() {
      await this.$store.dispatch('tickets/assignTicket', { id: this.ticketKey, agentId: this.user.id })
      EventBus.$emit('ticket:assigned', { ticketId: this.ticketKey, agentId: this.user.id })
    },
    onAssigned() {
      this.showAssign = false
      this.loadTicket()
    },
    onReplySent() {
      this.$store.commit('tickets/UPDATE_TICKET', Object.assign({}, this.ticket, { lastReplyAt: Date.now() }))
    },
    onTicketUpdated(payload) {
      if (payload.ticketId === this.ticketKey) {
        this.loadTicket()
      }
    },
    openCustomer(customer) {
      this.$router.push({ name: 'customer-detail', params: { id: customer.id } })
    }
  }
}
</script>
