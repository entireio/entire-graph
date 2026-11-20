<template>
  <div id="dashboard" class="dashboard">
    <section class="dashboard__stats">
      <stat-card
        v-for="stat in stats"
        :key="stat.key"
        :label="stat.label"
        :value="stat.value"
        :tone="stat.tone"
      />
    </section>
    <section class="dashboard__queue">
      <header class="dashboard__queue-header">
        <h2>My queue</h2>
        <base-button :busy="claiming" @click="claimNext">Claim next ticket</base-button>
      </header>
      <ticket-list :selected-id="lastClaimedId" :auto-refresh="false" />
    </section>
  </div>
</template>

<script>
import { mapState, mapGetters } from 'vuex'
import StatCard from '@/components/reports/StatCard.vue'
import TicketList from '@/components/tickets/TicketList.vue'
import { EventBus } from '@/eventBus'
import { slaStatus } from '@/utils/sla'

export default {
  name: 'Dashboard',
  components: {
    StatCard,
    TicketList
  },
  data() {
    return {
      claiming: false,
      lastClaimedId: null,
      assignedToday: 0,
      breachCount: 0
    }
  },
  computed: {
    ...mapState('auth', ['user']),
    ...mapGetters('tickets', ['openTickets', 'breachedTickets']),
    atRiskCount() {
      return this.openTickets.filter((ticket) => slaStatus(ticket) === 'at-risk').length
    },
    stats() {
      return [
        { key: 'open', label: 'Open', value: this.openTickets.length },
        { key: 'at-risk', label: 'At risk', value: this.atRiskCount, tone: 'warning' },
        { key: 'breached', label: 'Breached', value: this.breachedTickets.length + this.breachCount, tone: 'danger' },
        { key: 'assigned', label: 'Assigned today', value: this.assignedToday, tone: 'success' }
      ]
    }
  },
  created() {
    EventBus.$on('ticket:assigned', this.refreshStats)
    EventBus.$on('sla:breached', this.onBreach)
  },
  beforeDestroy() {
    EventBus.$off('ticket:assigned', this.refreshStats)
    EventBus.$off('sla:breached', this.onBreach)
  },
  methods: {
    refreshStats() {
      this.assignedToday += 1
    },
    onBreach() {
      this.breachCount += 1
    },
    async claimNext() {
      const next = this.openTickets.find((ticket) => !ticket.assigneeId)
      if (!next) {
        return
      }
      this.claiming = true
      try {
        await this.$store.dispatch('tickets/assignTicket', { id: next.id, agentId: this.user.id })
        this.lastClaimedId = next.id
        EventBus.$emit('ticket:assigned', { ticketId: next.id, agentId: this.user.id })
      } finally {
        this.claiming = false
      }
    }
  }
}
</script>
