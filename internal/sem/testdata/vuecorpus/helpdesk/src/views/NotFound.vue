<template>
  <div id="not-found" class="not-found">
    <h1>Page not found</h1>
    <p>The page you were looking for does not exist or you do not have access to it.</p>
    <base-button @click="goBack">Go back</base-button>
    <router-link :to="{ name: 'dashboard' }">Dashboard</router-link>
  </div>
</template>

<script>
export default {
  name: 'NotFound',
  methods: {
    goBack() {
      if (window.history.length > 1) {
        this.$router.back()
      } else {
        this.$router.push({ name: 'dashboard' })
      }
    }
  }
}
</script>
