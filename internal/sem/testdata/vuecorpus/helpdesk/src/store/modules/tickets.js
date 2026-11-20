import {
  fetchTicketList,
  fetchTicket as fetchTicketById,
  updateTicketStatus,
  assignTicketToAgent,
  fetchReplies,
  createReply
} from '../../api/tickets'
import { EventBus } from '../../eventBus'
import { comparePriority } from '../../utils/priority'
import { isBreached } from '../../utils/sla'

const state = () => ({
  items: [],
  total: 0,
  current: null,
  replies: [],
  loading: false,
  filters: {
    status: 'open',
    priority: '',
    assignee: '',
    query: ''
  }
})

const getters = {
  openTickets(state) {
    return state.items.filter((ticket) => ticket.status !== 'closed')
  },
  sortedTickets(state, getters) {
    return getters.openTickets.slice().sort(comparePriority)
  },
  breachedTickets(state) {
    const now = Date.now()
    return state.items.filter((ticket) => isBreached(ticket, now))
  },
  ticketById: (state) => (id) => state.items.find((ticket) => ticket.id === id)
}

const mutations = {
  SET_LOADING(state, loading) {
    state.loading = loading
  },
  SET_TICKETS(state, { items, total }) {
    state.items = items
    state.total = total
  },
  SET_CURRENT(state, ticket) {
    state.current = ticket
  },
  SET_REPLIES(state, replies) {
    state.replies = replies
  },
  ADD_REPLY(state, reply) {
    state.replies.push(reply)
  },
  SET_FILTERS(state, filters) {
    state.filters = Object.assign({}, state.filters, filters)
  },
  UPDATE_TICKET(state, ticket) {
    const index = state.items.findIndex((item) => item.id === ticket.id)
    if (index !== -1) {
      state.items.splice(index, 1, ticket)
    }
    if (state.current && state.current.id === ticket.id) {
      state.current = ticket
    }
  }
}

const actions = {
  async fetchTickets({ commit, state }, params) {
    commit('SET_LOADING', true)
    try {
      const result = await fetchTicketList(Object.assign({}, state.filters, params))
      commit('SET_TICKETS', result)
      return result
    } finally {
      commit('SET_LOADING', false)
    }
  },
  async fetchTicket({ commit, dispatch }, id) {
    const ticket = await fetchTicketById(id)
    commit('SET_CURRENT', ticket)
    await dispatch('fetchReplies', id)
    return ticket
  },
  async fetchReplies({ commit }, ticketId) {
    commit('SET_REPLIES', await fetchReplies(ticketId))
  },
  async postReply({ commit, dispatch }, { ticketId, body, internal }) {
    const reply = await createReply(ticketId, body, internal)
    commit('ADD_REPLY', reply)
    dispatch('notifications/push', { level: 'success', text: 'Reply sent' }, { root: true })
    return reply
  },
  async updateStatus({ commit }, { id, status }) {
    const ticket = await updateTicketStatus(id, status)
    commit('UPDATE_TICKET', ticket)
    EventBus.$emit('ticket:updated', { ticketId: id })
    return ticket
  },
  async assignTicket({ commit }, { id, agentId }) {
    const ticket = await assignTicketToAgent(id, agentId)
    commit('UPDATE_TICKET', ticket)
    return ticket
  },
  applyFilters({ commit, dispatch }, filters) {
    commit('SET_FILTERS', filters)
    return dispatch('fetchTickets', { page: 1 })
  }
}

export default {
  namespaced: true,
  state,
  getters,
  mutations,
  actions
}
