import { get, post, put } from './http'

export function fetchTicketList(params) {
  return get('/tickets', params)
}

export function fetchTicket(id) {
  return get('/tickets/' + id)
}

export function createTicket(payload) {
  return post('/tickets', payload)
}

export function updateTicketStatus(id, status) {
  return put('/tickets/' + id + '/status', { status })
}

export function assignTicketToAgent(id, agentId) {
  return put('/tickets/' + id + '/assignee', { agentId })
}

export function fetchReplies(ticketId) {
  return get('/tickets/' + ticketId + '/replies')
}

export function createReply(ticketId, body, internal) {
  return post('/tickets/' + ticketId + '/replies', { body, internal: Boolean(internal) })
}
