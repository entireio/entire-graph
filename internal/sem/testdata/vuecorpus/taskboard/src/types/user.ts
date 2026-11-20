export type Role = 'owner' | 'admin' | 'member' | 'viewer'

export interface User {
  id: string
  name: string
  email: string
  avatarUrl: string | null
  role: Role
}

export interface Credentials {
  email: string
  password: string
}

export interface Session {
  token: string
  user: User
  expiresAt: string
}

export function isPrivileged(user: User | null): boolean {
  return user !== null && (user.role === 'owner' || user.role === 'admin')
}
