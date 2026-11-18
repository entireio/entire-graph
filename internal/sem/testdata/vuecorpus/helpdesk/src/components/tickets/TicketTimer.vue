<template>
  <span class="ticket-timer" :class="'ticket-timer--' + status" :title="deadlineLabel">
    <template v-if="status === 'met'">Responded</template>
    <template v-else>{{ remainingLabel }}</template>
  </span>
</template>

<script src="./TicketTimer.js"></script>

<style scoped>
.ticket-timer--at-risk {
  color: #b26a00;
}
.ticket-timer--breached {
  color: #c62828;
  font-weight: 600;
}
</style>
