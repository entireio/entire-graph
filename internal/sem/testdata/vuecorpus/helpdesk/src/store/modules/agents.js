import { fetchAgents, fetchCannedResponses } from '../../api/agents'

const state = () => ({
  all: [],
  cannedResponses: []
})

const getters = {
  availableAgents(state) {
    return state.all.filter((agent) => agent.online && !agent.away)
  },
  agentName: (state) => (id) => {
    const agent = state.all.find((candidate) => candidate.id === id)
    return agent ? agent.name : 'Unassigned'
  }
}

const mutations = {
  SET_AGENTS(state, agents) {
    state.all = agents
  },
  SET_CANNED_RESPONSES(state, responses) {
    state.cannedResponses = responses
  }
}

const actions = {
  async loadAgents({ commit, state }) {
    if (state.all.length) {
      return state.all
    }
    const agents = await fetchAgents()
    commit('SET_AGENTS', agents)
    return agents
  },
  async loadCannedResponses({ commit, rootState }) {
    const responses = await fetchCannedResponses(rootState.auth.user.id)
    commit('SET_CANNED_RESPONSES', responses)
  }
}

export default {
  namespaced: true,
  state,
  getters,
  mutations,
  actions
}
