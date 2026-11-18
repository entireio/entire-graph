import Vue from 'vue'
import Vuex from 'vuex'
import tickets from './modules/tickets'
import customers from './modules/customers'
import agents from './modules/agents'
import auth from './modules/auth'
import notifications from './modules/notifications'

Vue.use(Vuex)

export default new Vuex.Store({
  strict: process.env.NODE_ENV !== 'production',
  modules: {
    tickets,
    customers,
    agents,
    auth,
    notifications
  }
})
