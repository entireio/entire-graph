import { slaTimerMixin } from '../../mixins/slaTimerMixin'
import { slaDeadline, slaStatus } from '../../utils/sla'
import { formatDate } from '../../utils/date'

export default {
  name: 'TicketTimer',
  mixins: [slaTimerMixin],
  props: {
    ticket: {
      type: Object,
      required: true
    },
    compact: {
      type: Boolean,
      default: false
    }
  },
  computed: {
    slaTicket() {
      return this.ticket
    },
    status() {
      return slaStatus(this.ticket, this.now)
    },
    deadlineLabel() {
      return 'Due ' + formatDate(slaDeadline(this.ticket), true)
    },
    remainingLabel() {
      return this.formatRemaining(this.slaMinutesLeft(this.ticket))
    }
  },
  methods: {
    formatRemaining(minutes) {
      if (minutes === null) {
        return ''
      }
      const abs = Math.abs(minutes)
      const text = abs >= 60 ? Math.floor(abs / 60) + 'h ' + (abs % 60) + 'm' : abs + 'm'
      if (minutes < 0) {
        return text + ' overdue'
      }
      return this.compact ? text : text + ' left'
    }
  }
}
