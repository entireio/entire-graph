<template>
  <tr
    class="ticket-row"
    :class="{ 'ticket-row--selected': selected, 'ticket-row--breached': breached }"
    data-id="row"
    v-bind:id="rowDomId"
    data-testid="ticket-row"
    @click="select"
  >
    <td class="ticket-row__subject">
      <priority-badge :priority="ticket.priority" />
      {{ ticket.subject | truncate(80) }}
    </td>
    <td>{{ ticket.customerName }}</td>
    <td>{{ ticket.createdAt | timeAgo }}</td>
    <td><ticket-timer :ticket="ticket" compact /></td>
    <td class="ticket-row__actions">
      <base-button variant="link" @click.stop="assign">Assign</base-button>
    </td>
  </tr>
</template>

<script>
import PriorityBadge from './PriorityBadge.vue'
import TicketTimer from './TicketTimer.vue'
import { slaTimerMixin } from '../../mixins/slaTimerMixin'
import { slaStatus } from '@/utils/sla'

export default {
  name: 'TicketRow',
  components: {
    PriorityBadge,
    TicketTimer
  },
  mixins: [slaTimerMixin],
  props: {
    ticket: {
      type: Object,
      required: true
    },
    selected: {
      type: Boolean,
      default: false
    }
  },
  computed: {
    slaTicket() {
      return this.ticket
    },
    breached() {
      return slaStatus(this.ticket, this.now) === 'breached'
    },
    rowDomId() {
      return 'ticket-row-' + this.ticket.id
    }
  },
  methods: {
    select() {
      this.$emit('select', this.ticket)
    },
    assign() {
      this.$emit('assign', this.ticket)
    }
  }
}
</script>
