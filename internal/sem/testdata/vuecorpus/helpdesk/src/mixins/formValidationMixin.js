import { isBlank, isEmail, exceedsLength } from '@/utils/validators'

export const formValidationMixin = {
  data() {
    return {
      errors: {}
    }
  },
  computed: {
    hasErrors() {
      return Object.keys(this.errors).length > 0
    }
  },
  methods: {
    setError(field, message) {
      this.$set(this.errors, field, message)
    },
    clearErrors() {
      this.errors = {}
    },
    validateRequired(field, value, label) {
      if (isBlank(value)) {
        this.setError(field, label + ' is required')
        return false
      }
      return true
    },
    validateEmail(field, value) {
      if (!isEmail(value)) {
        this.setError(field, 'Enter a valid email address')
        return false
      }
      return true
    },
    validateMaxLength(field, value, max) {
      if (exceedsLength(value, max)) {
        this.setError(field, 'Must be ' + max + ' characters or fewer')
        return false
      }
      return true
    }
  }
}

export default formValidationMixin
