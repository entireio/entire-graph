import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import BaseButton from '@/components/base/BaseButton.vue'
import BaseModal from '@/components/base/BaseModal.vue'
import { useAuthStore } from '@/stores/auth'
import { bus } from '@/utils/eventBus'

const app = createApp(App)

app.use(createPinia())
app.use(router)

app.component('BaseButton', BaseButton)
app.component('BaseModal', BaseModal)

async function bootstrap() {
  const auth = useAuthStore()
  await auth.restoreSession()
  bus.on('session:expired', () => {
    auth.applySession(null)
    router.push({ name: 'login' })
  })
  await router.isReady()
  app.mount('#app')
}

bootstrap()
