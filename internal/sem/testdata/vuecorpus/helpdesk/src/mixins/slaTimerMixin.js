import { EventBus } from '../eventBus'
import { isBreached, minutesRemaining } from '../utils/sla'

const TICK_MS = 30000

// Keeps `now` fresh so SLA countdowns re-render, and announces a breach once.
// The host component provides a `slaTicket` computed or prop.
export const slaTimerMixin = {
  data() {
    return {
      now: Date.now(),
      slaInterval: null,
      breachAnnounced: false
    }
  },
  created() {
    this.startSlaTimer()
  },
  beforeDestroy() {
    this.stopSlaTimer()
  },
  methods: {
    startSlaTimer() {
      this.stopSlaTimer()
      this.slaInterval = setInterval(this.tickSla, TICK_MS)
    },
    stopSlaTimer() {
      if (this.slaInterval) {
        clearInterval(this.slaInterval)
        this.slaInterval = null
      }
    },
    tickSla() {
      this.now = Date.now()
      if (this.slaTicket && !this.breachAnnounced && isBreached(this.slaTicket, this.now)) {
        this.breachAnnounced = true
        EventBus.$emit('sla:breached', this.slaTicket.id)
      }
    },
    slaMinutesLeft(ticket) {
      return minutesRemaining(ticket, this.now)
    }
  }
}

export default slaTimerMixin
