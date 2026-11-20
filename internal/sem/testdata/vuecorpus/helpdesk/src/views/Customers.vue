<template>
  <section class="customers-view">
    <header class="page-header">
      <h1>Customers</h1>
      <input
        id="customer-search"
        v-model.trim="search"
        type="search"
        placeholder="Search customers"
        @input="onSearch"
      >
    </header>
    <base-spinner v-if="loading" />
    <div v-else class="customer-grid">
      <customer-card
        v-for="customer in rows"
        :key="customer.id"
        :customer="customer"
        @open="openCustomer"
      />
    </div>
    <footer class="pager">
      <base-button variant="link" :disabled="!hasPrevPage" @click="prevPage">Previous</base-button>
      <span>Page {{ page }} of {{ totalPages }}</span>
      <base-button variant="link" :disabled="!hasNextPage" @click="nextPage">Next</base-button>
    </footer>
  </section>
</template>

<script>
import { mapActions } from 'vuex'
import BaseListView from '@/components/base/BaseListView.vue'
import CustomerCard from '@/components/customers/CustomerCard.vue'
import { debounce } from '@/utils/debounce'

export default {
  name: 'Customers',
  extends: BaseListView,
  components: {
    CustomerCard
  },
  data() {
    return {
      search: ''
    }
  },
  created() {
    this.debouncedSearch = debounce(this.reload, 300)
  },
  methods: {
    ...mapActions(['customers/fetchCustomers']),
    fetchPage() {
      return this.load(() => this['customers/fetchCustomers']({
        page: this.page,
        pageSize: this.pageSize,
        q: this.search
      }))
    },
    reload() {
      return this.goToPage(1)
    },
    onSearch() {
      this.debouncedSearch()
    },
    openCustomer(customer) {
      this.$router.push({ name: 'customer-detail', params: { id: customer.id } })
    }
  }
}
</script>
