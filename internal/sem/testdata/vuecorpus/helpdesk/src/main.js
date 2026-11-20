import Vue from 'vue'
import App from './App.vue'
import router from './router'
import store from './store'
import BaseButton from './components/base/BaseButton.vue'
import BaseModal from './components/base/BaseModal.vue'
import BaseSpinner from './components/base/BaseSpinner.vue'
import { registerFilters } from './utils/filters'

Vue.config.productionTip = false

Vue.component('base-button', BaseButton)
Vue.component('base-modal', BaseModal)
Vue.component('BaseSpinner', BaseSpinner)

registerFilters(Vue)

new Vue({
  router,
  store,
  render: (h) => h(App)
}).$mount('#app')
