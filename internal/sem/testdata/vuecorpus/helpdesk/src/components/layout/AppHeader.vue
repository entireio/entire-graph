<template>
  <header class="app-header">
    <base-button variant="link" aria-label="Toggle navigation" @click="$emit('toggle-sidebar')">Menu</base-button>
    <form class="app-header__search" role="search" @submit.prevent="search">
      <input id="global-search" v-model.trim="searchTerm" type="search" placeholder="Search tickets">
    </form>
    <span class="app-header__notifications" :title="unreadCount + ' unread'">{{ unreadCount }}</span>
    <span class="app-header__user">{{ user && user.name }}</span>
    <base-button variant="secondary" @click="logout">Sign out</base-button>
  </header>
</template>

<script>
import { mapState, mapGetters } from 'vuex'

export default {
  name: 'AppHeader',
  data() {
    return {
      searchTerm: ''
    }
  },
  computed: {
    ...mapState('auth', ['user']),
    ...mapGetters('notifications', ['unreadCount'])
  },
  methods: {
    search() {
      if (this.searchTerm) {
        this.$router.push({ name: 'tickets', query: { q: this.searchTerm } })
      }
    },
    async logout() {
      await this.$store.dispatch('auth/logout')
      this.$router.push({ name: 'login' })
    }
  }
}
</script>
