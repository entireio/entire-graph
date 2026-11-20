import { shallowMount, createLocalVue } from '@vue/test-utils'
import Vuex from 'vuex'
import TicketReplyForm from '@/components/tickets/TicketReplyForm.vue'
import { EventBus } from '@/eventBus'

const localVue = createLocalVue()
localVue.use(Vuex)

function mountForm(postReply) {
  const store = new Vuex.Store({
    modules: {
      tickets: {
        namespaced: true,
        actions: { postReply }
      }
    }
  })
  return shallowMount(TicketReplyForm, {
    localVue,
    store,
    propsData: { ticketId: 9 },
    stubs: ['base-button', 'canned-response-picker']
  })
}

describe('TicketReplyForm', () => {
  it('refuses to send an empty reply', async () => {
    const postReply = jest.fn()
    const wrapper = mountForm(postReply)
    await wrapper.vm.submitReply()
    expect(postReply).not.toHaveBeenCalled()
    expect(wrapper.vm.errors.body).toBe('Reply is required')
  })

  it('emits reply-sent and announces the reply on the bus', async () => {
    const reply = { id: 3, body: 'On it' }
    const postReply = jest.fn().mockResolvedValue(reply)
    const announced = jest.fn()
    EventBus.$on('reply:posted', announced)
    const wrapper = mountForm(postReply)
    await wrapper.setData({ body: 'On it' })

    await wrapper.vm.submitReply()

    expect(wrapper.emitted('reply-sent')[0]).toEqual([reply])
    expect(announced).toHaveBeenCalledWith({ ticketId: 9, reply })
    EventBus.$off('reply:posted', announced)
  })
})
