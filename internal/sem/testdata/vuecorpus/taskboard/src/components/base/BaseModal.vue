<script setup lang="ts">
import { onBeforeUnmount, onMounted } from 'vue'

const props = withDefaults(defineProps<{ title: string; width?: string }>(), { width: '32rem' })

const open = defineModel<boolean>('open', { default: false })

function close() {
  open.value = false
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && open.value) {
    close()
  }
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-backdrop" @click.self="close">
      <section class="modal" role="dialog" aria-modal="true" :aria-label="title" :style="{ width: props.width }">
        <header class="modal-header">
          <slot name="header" :close="close">
            <h2 class="modal-title">{{ title }}</h2>
          </slot>
          <button type="button" class="modal-close" aria-label="Close" @click="close">x</button>
        </header>
        <div class="modal-body">
          <slot />
        </div>
        <footer v-if="$slots.footer" class="modal-footer">
          <slot name="footer" :close="close" />
        </footer>
      </section>
    </div>
  </Teleport>
</template>
