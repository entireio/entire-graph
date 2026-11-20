let nextId = 1

const state = () => ({
  items: []
})

const getters = {
  unreadCount(state) {
    return state.items.filter((item) => !item.read).length
  }
}

const mutations = {
  PUSH(state, notification) {
    state.items.unshift(Object.assign({ id: nextId++, read: false, createdAt: Date.now() }, notification))
  },
  DISMISS(state, id) {
    state.items = state.items.filter((item) => item.id !== id)
  },
  MARK_ALL_READ(state) {
    state.items.forEach((item) => {
      item.read = true
    })
  }
}

const actions = {
  push({ commit }, notification) {
    commit('PUSH', notification)
  }
}

export default {
  namespaced: true,
  state,
  getters,
  mutations,
  actions
}
