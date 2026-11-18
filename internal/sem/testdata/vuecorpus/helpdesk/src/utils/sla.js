import { addMinutes, minutesBetween } from './date'

// Minutes allowed before the first response, by priority.
export const SLA_TARGETS = {
  urgent: 30,
  high: 120,
  normal: 480,
  low: 1440
}

export function slaTargetMinutes(priority) {
  return SLA_TARGETS[priority] || SLA_TARGETS.normal
}

export function slaDeadline(ticket) {
  return addMinutes(ticket.createdAt, slaTargetMinutes(ticket.priority))
}

export function minutesRemaining(ticket, now) {
  if (ticket.firstRespondedAt) {
    return null
  }
  return minutesBetween(now || Date.now(), slaDeadline(ticket))
}

export function isBreached(ticket, now) {
  const remaining = minutesRemaining(ticket, now)
  return remaining !== null && remaining < 0
}

export function slaStatus(ticket, now) {
  const remaining = minutesRemaining(ticket, now)
  if (remaining === null) {
    return 'met'
  }
  if (remaining < 0) {
    return 'breached'
  }
  return remaining < 30 ? 'at-risk' : 'on-track'
}
