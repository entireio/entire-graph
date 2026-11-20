<template>
  <article class="customer-card">
    <header>
      <h3>{{ customer.name }}</h3>
      <span class="customer-card__plan">{{ customer.plan }}</span>
    </header>
    <dl>
      <dt>Email</dt>
      <dd><a :href="'mailto:' + customer.email">{{ customer.email }}</a></dd>
      <dt>Customer since</dt>
      <dd>{{ since }}</dd>
      <dt>Open tickets</dt>
      <dd>{{ customer.openTickets || 0 }}</dd>
    </dl>
    <base-button variant="link" @click="open">View profile</base-button>
  </article>
</template>

<script>
import { formatDate } from '@/utils/date'

export default {
  name: 'CustomerCard',
  props: {
    customer: {
      type: Object,
      required: true
    }
  },
  computed: {
    since() {
      return formatDate(this.customer.createdAt)
    }
  },
  methods: {
    open() {
      this.$emit('open', this.customer)
    }
  }
}
</script>
