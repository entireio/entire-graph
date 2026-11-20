import { defineStore } from 'pinia'

export type Theme = 'light' | 'dark'

export type Density = 'comfortable' | 'compact'

export const useSettingsStore = defineStore('settings', {
  state: () => ({
    theme: 'light' as Theme,
    density: 'comfortable' as Density,
    showCompleted: true,
    pageSize: 20,
  }),
  getters: {
    isDark: (state) => state.theme === 'dark',
  },
  actions: {
    toggleTheme() {
      this.theme = this.theme === 'light' ? 'dark' : 'light'
    },
    setDensity(density: Density) {
      this.density = density
    },
    setPageSize(size: number) {
      this.pageSize = Math.max(5, Math.min(100, size))
    },
  },
})
