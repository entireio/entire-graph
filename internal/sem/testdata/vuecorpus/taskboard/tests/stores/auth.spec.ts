import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAuthStore } from '@/stores/auth'
import { postLogin } from '@/api/auth'
import { makeUser } from '../fixtures'

vi.mock('@/api/auth', () => ({
  postLogin: vi.fn(),
  postLogout: vi.fn(async () => undefined),
  fetchSession: vi.fn(async () => null),
}))

describe('useAuthStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('stores the session after login', async () => {
    vi.mocked(postLogin).mockResolvedValue({ token: 'abc', user: makeUser({ role: 'admin' }), expiresAt: '2030-01-01' })
    const auth = useAuthStore()
    await auth.login({ email: 'ada@example.com', password: 'secret' })
    expect(auth.isAuthenticated).toBe(true)
    expect(auth.isAdmin).toBe(true)
  })

  it('clears the session on logout', async () => {
    const auth = useAuthStore()
    auth.applySession({ token: 'abc', user: makeUser(), expiresAt: '2030-01-01' })
    await auth.logout()
    expect(auth.currentUser).toBeNull()
  })
})
