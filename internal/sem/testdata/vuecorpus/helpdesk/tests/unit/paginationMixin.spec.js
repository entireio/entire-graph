import { shallowMount } from '@vue/test-utils'
import { paginationMixin } from '@/mixins/paginationMixin'

function mountPaged(total) {
  return shallowMount({
    template: '<div />',
    mixins: [paginationMixin],
    data() {
      return { fetched: [] }
    },
    created() {
      this.setTotal(total)
    },
    methods: {
      fetchPage() {
        this.fetched.push(this.page)
        return Promise.resolve()
      }
    }
  })
}

describe('paginationMixin', () => {
  it('computes the page count from the total', () => {
    const wrapper = mountPaged(120)
    expect(wrapper.vm.totalPages).toBe(5)
  })

  it('nextPage advances and fetches', async () => {
    const wrapper = mountPaged(60)
    await wrapper.vm.nextPage()
    expect(wrapper.vm.page).toBe(2)
    expect(wrapper.vm.fetched).toEqual([2])
  })

  it('goToPage clamps to the last page', async () => {
    const wrapper = mountPaged(30)
    await wrapper.vm.goToPage(9)
    expect(wrapper.vm.page).toBe(2)
  })
})
