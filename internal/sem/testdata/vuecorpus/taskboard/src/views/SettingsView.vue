<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { useSettingsStore, type Density } from '@/stores/settings'

const settings = useSettingsStore()
const { theme, density, pageSize, showCompleted } = storeToRefs(settings)

const densities: Density[] = ['comfortable', 'compact']

function onDensityChange(value: Density) {
  settings.setDensity(value)
}

function onPageSizeInput(event: Event) {
  settings.setPageSize(Number((event.target as HTMLInputElement).value))
}
</script>

<template>
  <section class="settings-view">
    <h1>Settings</h1>
    <div class="settings-row">
      <span>Theme: {{ theme }}</span>
      <base-button @click="settings.toggleTheme()">Toggle theme</base-button>
    </div>
    <fieldset class="settings-row">
      <legend>Density</legend>
      <label v-for="option in densities" :key="option">
        <input type="radio" name="density" :value="option" :checked="density === option" @change="onDensityChange(option)" />
        {{ option }}
      </label>
    </fieldset>
    <label class="settings-row" for="page-size">Page size</label>
    <input id="page-size" type="number" min="5" max="100" :value="pageSize" @input="onPageSizeInput" />
    <label class="settings-row">
      <input v-model="showCompleted" type="checkbox" />
      Show completed tasks
    </label>
  </section>
</template>
