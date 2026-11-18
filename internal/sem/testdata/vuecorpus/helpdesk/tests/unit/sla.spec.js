import { isBreached, minutesRemaining, slaStatus } from '@/utils/sla'

const CREATED = new Date('2024-03-01T09:00:00Z').getTime()

describe('sla utils', () => {
  it('counts down from the priority target', () => {
    const ticket = { createdAt: CREATED, priority: 'high' }
    expect(minutesRemaining(ticket, CREATED + 20 * 60000)).toBe(100)
  })

  it('stops counting once the ticket has a first response', () => {
    const ticket = { createdAt: CREATED, priority: 'urgent', firstRespondedAt: CREATED + 60000 }
    expect(minutesRemaining(ticket, CREATED + 90 * 60000)).toBeNull()
    expect(slaStatus(ticket, CREATED + 90 * 60000)).toBe('met')
  })

  it('reports a breach after the deadline', () => {
    const ticket = { createdAt: CREATED, priority: 'urgent' }
    expect(isBreached(ticket, CREATED + 31 * 60000)).toBe(true)
    expect(slaStatus(ticket, CREATED + 10 * 60000)).toBe('at-risk')
  })
})
