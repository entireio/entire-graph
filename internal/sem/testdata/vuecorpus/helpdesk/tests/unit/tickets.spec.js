import tickets from '@/store/modules/tickets'
import { fetchTicketList } from '@/api/tickets'

jest.mock('@/api/tickets')

describe('tickets store module', () => {
  it('SET_TICKETS replaces the list and the total', () => {
    const state = tickets.state()
    tickets.mutations.SET_TICKETS(state, { items: [{ id: 1 }, { id: 2 }], total: 2 })
    expect(state.items).toHaveLength(2)
    expect(state.total).toBe(2)
  })

  it('openTickets leaves out closed tickets', () => {
    const state = tickets.state()
    state.items = [{ id: 1, status: 'open' }, { id: 2, status: 'closed' }]
    expect(tickets.getters.openTickets(state)).toEqual([{ id: 1, status: 'open' }])
  })

  it('fetchTickets merges filters into the request and commits the result', async () => {
    const result = { items: [{ id: 7 }], total: 1 }
    fetchTicketList.mockResolvedValue(result)
    const commit = jest.fn()
    const state = tickets.state()

    await tickets.actions.fetchTickets({ commit, state }, { page: 2 })

    expect(fetchTicketList).toHaveBeenCalledWith(expect.objectContaining({ status: 'open', page: 2 }))
    expect(commit).toHaveBeenCalledWith('SET_TICKETS', result)
    expect(commit).toHaveBeenLastCalledWith('SET_LOADING', false)
  })

  it('applyFilters resets to the first page', async () => {
    const commit = jest.fn()
    const dispatch = jest.fn().mockResolvedValue(null)

    await tickets.actions.applyFilters({ commit, dispatch }, { priority: 'urgent' })

    expect(commit).toHaveBeenCalledWith('SET_FILTERS', { priority: 'urgent' })
    expect(dispatch).toHaveBeenCalledWith('fetchTickets', { page: 1 })
  })
})
