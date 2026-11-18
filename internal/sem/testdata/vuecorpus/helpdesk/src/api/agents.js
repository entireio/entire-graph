import { get, post } from './http'

export function fetchAgents() {
  return get('/agents')
}

export function fetchCannedResponses(agentId) {
  return get('/agents/' + agentId + '/canned-responses')
}

export function login(email, password) {
  return post('/auth/login', { email, password })
}

export function fetchCurrentAgent() {
  return get('/auth/me')
}
