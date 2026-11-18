import { fetchCustomers, fetchCustomer, updateCustomer, fetchCustomerTickets } from '@/api/customers'
import { formatDate } from '@/utils/date'

const state = () => ({
  list: [],
  total: 0,
  selected: null,
  tickets: []
})

const getters = {
  customerOptions(state) {
    return state.list.map((customer) => ({ value: customer.id, label: customer.name }))
  },
  customerSince(state) {
    return state.selected ? formatDate(state.selected.createdAt) : ''
  }
}

const mutations = {
  SET_CUSTOMERS(state, { items, total }) {
    state.list = items
    state.total = total
  },
  SET_SELECTED(state, customer) {
    state.selected = customer
  },
  SET_CUSTOMER_TICKETS(state, tickets) {
    state.tickets = tickets
  }
}

const actions = {
  async fetchCustomers({ commit }, params) {
    const result = await fetchCustomers(params)
    commit('SET_CUSTOMERS', result)
    return result
  },
  async selectCustomer({ commit }, id) {
    const customer = await fetchCustomer(id)
    commit('SET_SELECTED', customer)
    commit('SET_CUSTOMER_TICKETS', await fetchCustomerTickets(id))
  },
  async saveCustomer({ commit }, { id, changes }) {
    const customer = await updateCustomer(id, changes)
    commit('SET_SELECTED', customer)
    return customer
  }
}

export default {
  namespaced: true,
  state,
  getters,
  mutations,
  actions
}
