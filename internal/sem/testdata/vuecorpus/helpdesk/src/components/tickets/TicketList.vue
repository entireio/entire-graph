<template>
  <div id="ticket-list" class="ticket-list" tabindex="0" @keydown="handleKeydown">
    <ticket-filters :value="filters" @change="onFiltersChanged" />
    <base-spinner v-if="loading" />
    <table v-else class="ticket-table">
      <thead>
        <tr>
          <th>Subject</th>
          <th>Customer</th>
          <th>Opened</th>
          <th>SLA</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <ticket-row
          v-for="ticket in sortedTickets"
          :key="ticket.id"
          :ticket="ticket"
          :selected="ticket.id === selectedId"
          @select="selectTicket"
          @assign="openAssignDialog"
        />
      </tbody>
    </table>
    <footer class="ticket-list__pager">
      <base-button variant="link" :disabled="!hasPrevPage" @click="prevPage">Previous</base-button>
      <span>{{ page }} / {{ totalPages }}</span>
      <base-button variant="link" :disabled="!hasNextPage" @click="nextPage">Next</base-button>
    </footer>
    <base-modal :open="assigning !== null" title="Assign ticket" @close="assigning = null">
      <agent-select :ticket-id="assigningTicketId" @assigned="onAssigned" />
    </base-modal>
  </div>
</template>

<script>
import { mapState, mapGetters, mapActions } from 'vuex'
import TicketRow from './TicketRow.vue'
import TicketFilters from './TicketFilters.vue'
import AgentSelect from '../agents/AgentSelect.vue'
import { paginationMixin } from '@/mixins/paginationMixin'

const REFRESH_MS = 60000

export default {
  name: 'TicketList',
  components: {
    TicketRow,
    TicketFilters,
    AgentSelect
  },
  mixins: [paginationMixin],
  props: {
    selectedId: {
      type: [Number, String],
      default: null
    },
    autoRefresh: {
      type: Boolean,
      default: true
    }
  },
  data() {
    return {
      assigning: null,
      refreshTimer: null
    }
  },
  computed: {
    ...mapState('tickets', ['loading', 'filters']),
    ...mapGetters('tickets', ['sortedTickets']),
    assigningTicketId() {
      return this.assigning ? this.assigning.id : null
    }
  },
  watch: {
    filters: {
      handler() {
        this.goToPage(1)
      },
      deep: true
    }
  },
  created() {
    this.fetchPage()
  },
  mounted() {
    if (this.autoRefresh) {
      this.refreshTimer = setInterval(this.refresh, REFRESH_MS)
    }
  },
  beforeDestroy() {
    clearInterval(this.refreshTimer)
  },
  methods: {
    ...mapActions('tickets', ['fetchTickets', 'applyFilters']),
    async fetchPage() {
      const result = await this.fetchTickets({ page: this.page, pageSize: this.pageSize })
      this.setTotal(result.total)
      return result
    },
    refresh() {
      return this.fetchPage()
    },
    onFiltersChanged(filters) {
      this.applyFilters(filters)
    },
    selectTicket(ticket) {
      this.$router.push({ name: 'ticket-detail', params: { id: ticket.id } })
    },
    openAssignDialog(ticket) {
      this.assigning = ticket
    },
    onAssigned() {
      this.assigning = null
      this.refresh()
    },
    handleKeydown(event) {
      if (event.key === 'ArrowRight') {
        this.nextPage()
      } else if (event.key === 'ArrowLeft') {
        this.prevPage()
      }
    }
  }
}
</script>
