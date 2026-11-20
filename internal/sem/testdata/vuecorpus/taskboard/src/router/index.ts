import { createRouter, createWebHistory, type RouteLocationNormalized, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import LoginView from '@/views/LoginView.vue'

const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
  { path: '/', name: 'projects', component: () => import('@/views/ProjectsView.vue') },
  {
    path: '/projects/:projectId/board',
    name: 'board',
    component: () => import('@/views/BoardView.vue'),
    props: true,
  },
  { path: '/tasks/:taskId', name: 'task', component: () => import('@/views/TaskView.vue') },
  { path: '/settings', name: 'settings', component: () => import('../views/SettingsView.vue') },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
    meta: { public: true },
  },
]

export function isPublicRoute(route: RouteLocationNormalized): boolean {
  return route.meta.public === true
}

export async function authGuard(to: RouteLocationNormalized) {
  const auth = useAuthStore()
  if (!auth.isAuthenticated && !auth.restoring) {
    await auth.restoreSession()
  }
  if (!isPublicRoute(to) && !auth.isAuthenticated) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (to.name === 'login' && auth.isAuthenticated) {
    return { name: 'projects' }
  }
  return true
}

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
})

router.beforeEach(authGuard)

export default router
