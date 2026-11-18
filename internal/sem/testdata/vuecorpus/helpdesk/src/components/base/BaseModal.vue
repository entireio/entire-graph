<template>
  <div v-if="open" class="modal-backdrop" @click.self="close">
    <div class="modal" role="dialog" aria-modal="true" :aria-labelledby="titleId">
      <header class="modal__header">
        <h2 :id="titleId">{{ title }}</h2>
        <base-button variant="link" aria-label="Close" @click="close">x</base-button>
      </header>
      <div class="modal__body">
        <slot />
      </div>
      <footer v-if="$slots.footer" class="modal__footer">
        <slot name="footer" />
      </footer>
    </div>
  </div>
</template>

<script>
export default {
  name: 'BaseModal',
  props: {
    open: {
      type: Boolean,
      default: false
    },
    title: {
      type: String,
      default: ''
    }
  },
  computed: {
    titleId() {
      return 'modal-title-' + this._uid
    }
  },
  watch: {
    open(isOpen) {
      document.body.classList.toggle('has-modal', isOpen)
    }
  },
  mounted() {
    document.addEventListener('keydown', this.onKeydown)
  },
  beforeDestroy() {
    document.removeEventListener('keydown', this.onKeydown)
    document.body.classList.remove('has-modal')
  },
  methods: {
    close() {
      this.$emit('close')
    },
    onKeydown(event) {
      if (this.open && event.key === 'Escape') {
        this.close()
      }
    }
  }
}
</script>
