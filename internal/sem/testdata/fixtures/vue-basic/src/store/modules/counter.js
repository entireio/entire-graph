const mutations = {
  SET_COUNT(state, value) {
    state.count = value
  }
}

const getters = {
  total(state) {
    return state.count
  }
}

const actions = {
  save({ commit }, value) {
    commit('SET_COUNT', value)
  }
}

export default {
  namespaced: true,
  state: () => ({ count: 0 }),
  getters,
  mutations,
  actions
}
