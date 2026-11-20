import { shallowMount } from '@vue/test-utils'
import TicketRow from '@/components/tickets/TicketRow.vue'

const ticket = {
  id: 42,
  subject: 'Printer is on fire',
  customerName: 'Acme',
  priority: 'urgent',
  createdAt: Date.now()
}

function mountRow(propsData) {
  return shallowMount(TicketRow, {
    propsData: Object.assign({ ticket }, propsData),
    filters: {
      truncate: (value) => value,
      timeAgo: () => 'just now'
    },
    stubs: ['base-button']
  })
}

describe('TicketRow', () => {
  it('emits select with the ticket when the row is clicked', async () => {
    const wrapper = mountRow()
    await wrapper.trigger('click')
    expect(wrapper.emitted('select')[0]).toEqual([ticket])
  })

  it('emits assign from the assign button without selecting', () => {
    const wrapper = mountRow()
    wrapper.vm.assign()
    expect(wrapper.emitted('assign')[0]).toEqual([ticket])
    expect(wrapper.emitted('select')).toBeUndefined()
  })

  it('marks the selected row', () => {
    const wrapper = mountRow({ selected: true })
    expect(wrapper.classes()).toContain('ticket-row--selected')
  })
})
