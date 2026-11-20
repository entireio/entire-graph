<template>
  <aside class="notification-tray" :class="{ 'notification-tray--open': open }">
    <base-button variant="link" @click="toggle">Notifications ({{ unreadCount }})</base-button>
    <ul v-if="open" id="notification-list">
      <li v-for="item in items" :key="item.id" :class="'notification--' + item.level">
        <span>{{ item.text }}</span>
        <small>{{ item.createdAt | timeAgo }}</small>
        <base-button variant="link" aria-label="Dismiss" @click="DISMISS(item.id)">x</base-button>
      </li>
    </ul>
  </aside>
</template>

<script>
import { mapState, mapGetters, mapMutations, mapActions } from 'vuex'
import { EventBus } from '../../eventBus'

export default {
  name: 'NotificationTray',
  data() {
    return {
      open: false
    }
  },
  computed: {
    ...mapState('notifications', ['items']),
    ...mapGetters('notifications', ['unreadCount'])
  },
  created() {
    EventBus.$on('ticket:assigned', this.onAssigned)
    EventBus.$on('sla:breached', this.onBreached)
  },
  beforeDestroy() {
    EventBus.$off('ticket:assigned', this.onAssigned)
    EventBus.$off('sla:breached', this.onBreached)
  },
  methods: {
    ...mapMutations('notifications', ['DISMISS', 'MARK_ALL_READ']),
    ...mapActions('notifications', ['push']),
    toggle() {
      this.open = !this.open
      if (this.open) {
        this.MARK_ALL_READ()
      }
    },
    onAssigned(payload) {
      this.push({ level: 'info', text: 'Ticket #' + payload.ticketId + ' was assigned' })
    },
    onBreached(ticketId) {
      this.push({ level: 'danger', text: 'Ticket #' + ticketId + ' breached its SLA' })
    }
  }
}
</script>
