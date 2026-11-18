import { login, fetchCurrentAgent } from '../../api/agents'
import { getToken, setToken } from '../../api/http'

const state = () => ({
  user: null,
  token: getToken()
})

const getters = {
  isAuthenticated(state) {
    return Boolean(state.token && state.user)
  },
  hasToken(state) {
    return Boolean(state.token)
  },
  isSupervisor(state) {
    return Boolean(state.user && state.user.role === 'supervisor')
  }
}

const mutations = {
  SET_SESSION(state, { token, user }) {
    state.token = token
    state.user = user
  },
  CLEAR_SESSION(state) {
    state.token = null
    state.user = null
  }
}

const actions = {
  async signIn({ commit }, { email, password }) {
    const session = await login(email, password)
    setToken(session.token)
    commit('SET_SESSION', session)
    return session.user
  },
  async restoreSession({ commit, state }) {
    const user = await fetchCurrentAgent()
    commit('SET_SESSION', { token: state.token, user })
    return user
  },
  logout({ commit }) {
    setToken(null)
    commit('CLEAR_SESSION')
  }
}

export default {
  namespaced: true,
  state,
  getters,
  mutations,
  actions
}
