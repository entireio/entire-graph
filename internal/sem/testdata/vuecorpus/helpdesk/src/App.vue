<template>
  <div id="helpdesk-app" :class="{ 'layout--collapsed': sidebarCollapsed }">
    <template v-if="isAuthenticated">
      <app-header @toggle-sidebar="toggleSidebar" />
      <app-sidebar :collapsed="sidebarCollapsed" />
    </template>
    <main id="main-content">
      <router-view />
    </main>
    <notification-tray v-if="isAuthenticated" />
  </div>
</template>

<script>
import { mapGetters } from 'vuex'
import AppHeader from './components/layout/AppHeader.vue'
import AppSidebar from './components/layout/AppSidebar.vue'
import NotificationTray from './components/notifications/NotificationTray.vue'

const COLLAPSE_KEY = 'helpdesk.sidebarCollapsed'

export default {
  name: 'App',
  components: {
    AppHeader,
    AppSidebar,
    NotificationTray
  },
  data() {
    return {
      sidebarCollapsed: window.localStorage.getItem(COLLAPSE_KEY) === '1'
    }
  },
  computed: {
    ...mapGetters('auth', ['isAuthenticated'])
  },
  watch: {
    sidebarCollapsed(value) {
      window.localStorage.setItem(COLLAPSE_KEY, value ? '1' : '0')
    }
  },
  methods: {
    toggleSidebar() {
      this.sidebarCollapsed = !this.sidebarCollapsed
    }
  }
}
</script>

<style>
#helpdesk-app {
  display: grid;
  grid-template-columns: 220px 1fr;
  min-height: 100vh;
}
#helpdesk-app.layout--collapsed {
  grid-template-columns: 56px 1fr;
}
</style>
