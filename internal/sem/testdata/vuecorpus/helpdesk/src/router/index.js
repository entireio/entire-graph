import Vue from 'vue'
import VueRouter from 'vue-router'
import store from '@/store'
import Dashboard from '@/views/Dashboard.vue'

Vue.use(VueRouter)

const routes = [
  {
    path: '/',
    name: 'dashboard',
    component: Dashboard,
    meta: { requiresAuth: true }
  },
  {
    path: '/tickets',
    name: 'tickets',
    component: () => import(/* webpackChunkName: "tickets" */ '@/views/Tickets.vue'),
    meta: { requiresAuth: true }
  },
  {
    path: '/tickets/:id',
    name: 'ticket-detail',
    component: () => import(/* webpackChunkName: "tickets" */ '@/views/TicketDetail.vue'),
    props: true,
    meta: { requiresAuth: true }
  },
  {
    path: '/customers',
    name: 'customers',
    component: () => import(/* webpackChunkName: "customers" */ '@/views/Customers.vue'),
    meta: { requiresAuth: true }
  },
  {
    path: '/customers/:id',
    name: 'customer-detail',
    component: () => import(/* webpackChunkName: "customers" */ '../views/CustomerDetail.vue'),
    props: true,
    meta: { requiresAuth: true }
  },
  {
    path: '/reports',
    name: 'reports',
    component: () => import(/* webpackChunkName: "reports" */ '@/views/Reports.vue'),
    meta: { requiresAuth: true, supervisorOnly: true }
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/Login.vue')
  },
  {
    path: '*',
    name: 'not-found',
    component: () => import('@/views/NotFound.vue')
  }
]

const router = new VueRouter({
  mode: 'history',
  base: process.env.BASE_URL,
  routes
})

export async function authGuard(to, from, next) {
  if (!to.matched.some((record) => record.meta.requiresAuth)) {
    return next()
  }
  if (!store.getters['auth/isAuthenticated'] && store.getters['auth/hasToken']) {
    try {
      await store.dispatch('auth/restoreSession')
    } catch (error) {
      return next({ name: 'login', query: { redirect: to.fullPath } })
    }
  }
  if (!store.getters['auth/isAuthenticated']) {
    return next({ name: 'login', query: { redirect: to.fullPath } })
  }
  if (to.matched.some((record) => record.meta.supervisorOnly) && !store.getters['auth/isSupervisor']) {
    return next({ name: 'dashboard' })
  }
  return next()
}

router.beforeEach(authGuard)

export default router
