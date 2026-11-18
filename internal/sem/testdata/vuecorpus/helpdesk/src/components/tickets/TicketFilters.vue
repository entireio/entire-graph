<template>
  <form id="ticket-filters" class="ticket-filters" v-on:submit.prevent="apply">
    <label for="filter-status">Status</label>
    <select id="filter-status" v-model="local.status">
      <option v-for="option in statusOptions" :key="option.value" :value="option.value">
        {{ option.label }}
      </option>
    </select>
    <label :for="priorityInputId">Priority</label>
    <select :id="priorityInputId" v-model="local.priority">
      <option value="">Any</option>
      <option v-for="priority in priorities" :key="priority" :value="priority">
        {{ priority | priority }}
      </option>
    </select>
    <input
      id="filter-query"
      v-model.trim="local.query"
      type="search"
      placeholder="Search subject or customer"
      @keyup.esc="reset"
    >
    <base-button type="submit" :disabled="!isDirty">Apply</base-button>
    <base-button variant="link" @click="reset">Reset</base-button>
  </form>
</template>

<script>
import { PRIORITIES } from '../../utils/priority'

const DEFAULT_FILTERS = {
  status: 'open',
  priority: '',
  assignee: '',
  query: ''
}

export default {
  name: 'TicketFilters',
  props: {
    value: {
      type: Object,
      required: true
    }
  },
  data() {
    return {
      local: Object.assign({}, this.value),
      priorities: PRIORITIES,
      statusOptions: [
        { value: 'open', label: 'Open' },
        { value: 'pending', label: 'Pending' },
        { value: 'solved', label: 'Solved' },
        { value: 'closed', label: 'Closed' }
      ]
    }
  },
  computed: {
    priorityInputId() {
      return 'filter-priority-' + this._uid
    },
    isDirty() {
      return JSON.stringify(this.local) !== JSON.stringify(this.value)
    }
  },
  watch: {
    value: {
      handler(next) {
        this.local = Object.assign({}, next)
      },
      deep: true
    }
  },
  methods: {
    apply() {
      this.$emit('change', Object.assign({}, this.local))
    },
    reset() {
      this.local = Object.assign({}, DEFAULT_FILTERS)
      this.apply()
    }
  }
}
</script>
