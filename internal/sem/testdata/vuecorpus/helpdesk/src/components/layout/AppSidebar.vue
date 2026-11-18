<template>
  <nav
    class="app-sidebar"
    :class="{ 'app-sidebar--collapsed': collapsed }"
    data-id="main-nav"
    aria-label="Main navigation"
  >
    <router-link
      v-for="link in visibleLinks"
      :key="link.name"
      :to="{ name: link.name }"
      class="app-sidebar__link"
      active-class="is-active"
    >
      {{ link.label }}
      <span v-if="link.name === 'tickets' && breachedTickets.length" class="badge badge--danger">
        {{ breachedTickets.length }}
      </span>
    </router-link>
  </nav>
</template>

<script>
import { mapGetters } from 'vuex'

const LINKS = [
  { name: 'dashboard', label: 'Dashboard' },
  { name: 'tickets', label: 'Tickets' },
  { name: 'customers', label: 'Customers' },
  { name: 'reports', label: 'Reports', supervisorOnly: true }
]

export default {
  name: 'AppSidebar',
  props: {
    collapsed: {
      type: Boolean,
      default: false
    }
  },
  computed: {
    ...mapGetters('auth', ['isSupervisor']),
    ...mapGetters('tickets', ['breachedTickets']),
    visibleLinks() {
      return LINKS.filter((link) => !link.supervisorOnly || this.isSupervisor)
    }
  }
}
</script>
