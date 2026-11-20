import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import TaskCard from '@/components/TaskCard.vue'
import { makeTask } from '../fixtures'

describe('TaskCard', () => {
  it('emits select when clicked', async () => {
    const task = makeTask()
    const wrapper = mount(TaskCard, { props: { task } })
    await wrapper.trigger('click')
    expect(wrapper.emitted('select')?.[0]).toEqual([task])
  })

  it('emits toggle-done from the checkbox', async () => {
    const wrapper = mount(TaskCard, { props: { task: makeTask({ id: 't3' }) } })
    await wrapper.find('input[type="checkbox"]').trigger('change')
    expect(wrapper.emitted('toggle-done')?.[0]).toEqual(['t3', true])
  })
})
