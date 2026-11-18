import { defineStore } from 'pinia'
import { fetchSession, postLogin, postLogout } from '@/api/auth'
import { setAuthToken } from '@/api/client'
import { isPrivileged, type Credentials, type Session, type User } from '@/types/user'

interface AuthState {
  session: Session | null
  restoring: boolean
  lastError: string | null
}

export const useAuthStore = defineStore('auth', {
  state: (): AuthState => ({
    session: null,
    restoring: false,
    lastError: null,
  }),
  getters: {
    isAuthenticated: (state): boolean => state.session !== null,
    currentUser: (state): User | null => state.session?.user ?? null,
    isAdmin(): boolean {
      return isPrivileged(this.currentUser)
    },
  },
  actions: {
    async login(credentials: Credentials) {
      this.lastError = null
      try {
        const session = await postLogin(credentials)
        this.applySession(session)
      } catch (err) {
        this.lastError = err instanceof Error ? err.message : 'Login failed'
        throw err
      }
    },
    async logout() {
      await postLogout()
      this.applySession(null)
    },
    async restoreSession() {
      this.restoring = true
      try {
        this.applySession(await fetchSession())
      } finally {
        this.restoring = false
      }
    },
    applySession(session: Session | null) {
      this.session = session
      setAuthToken(session?.token ?? null)
    },
  },
})
