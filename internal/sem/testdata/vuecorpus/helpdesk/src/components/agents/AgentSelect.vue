<template>
  <div class="agent-select">
    <label :for="inputId">Assign to</label>
    <select :id="inputId" v-model="agentId" :disabled="saving">
      <option disabled value="">Choose an agent</option>
      <option v-for="agent in availableAgents" :key="agent.id" :value="agent.id">
        {{ agent.name }}
      </option>
    </select>
    <base-button :busy="saving" :disabled="!agentId" @click="assign">Assign</base-button>
  </div>
</template>

<script>
import { mapGetters } from 'vuex'
import { EventBus } from '@/eventBus'

export default {
  name: 'AgentSelect',
  props: {
    ticketId: {
      type: [Number, String],
      required: true
    }
  },
  data() {
    return {
      agentId: '',
      saving: false
    }
  },
  computed: {
    ...mapGetters('agents', ['availableAgents']),
    inputId() {
      return 'agent-select-' + this.ticketId
    }
  },
  created() {
    this.$store.dispatch('agents/loadAgents')
  },
  methods: {
    async assign() {
      this.saving = true
      try {
        await this.$store.dispatch('tickets/assignTicket', { id: this.ticketId, agentId: this.agentId })
        EventBus.$emit('ticket:assigned', { ticketId: this.ticketId, agentId: this.agentId })
        this.$emit('assigned', this.agentId)
      } finally {
        this.saving = false
      }
    }
  }
}
</script>
