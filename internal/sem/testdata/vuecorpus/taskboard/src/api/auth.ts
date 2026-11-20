import { request } from './client'
import type { Credentials, Session } from '../types/user'

export function postLogin(credentials: Credentials): Promise<Session> {
  return request<Session>('POST', '/auth/login', credentials)
}

export function postLogout(): Promise<void> {
  return request<void>('POST', '/auth/logout')
}

export function fetchSession(): Promise<Session | null> {
  return request<Session | null>('GET', '/auth/session')
}
