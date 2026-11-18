<template>
  <div class="login-page">
    <form id="login-form" class="login-form" v-on:submit.prevent="signIn">
      <h1>Sign in to Helpdesk</h1>
      <label for="login-email">Email</label>
      <input id="login-email" v-model.trim="email" type="email" autocomplete="username">
      <p v-if="errors.email" class="field-error">{{ errors.email }}</p>
      <label for="login-password">Password</label>
      <input id="login-password" v-model="password" type="password" autocomplete="current-password">
      <p v-if="errors.password" class="field-error">{{ errors.password }}</p>
      <p v-if="failure" class="form-error" role="alert">{{ failure }}</p>
      <base-button type="submit" :busy="busy">Sign in</base-button>
    </form>
  </div>
</template>

<script>
import { mapActions } from 'vuex'
import { formValidationMixin } from '../mixins/formValidationMixin'

export default {
  name: 'Login',
  mixins: [formValidationMixin],
  data() {
    return {
      email: '',
      password: '',
      busy: false,
      failure: ''
    }
  },
  methods: {
    ...mapActions('auth', { authenticate: 'signIn' }),
    validate() {
      this.clearErrors()
      const emailOk = this.validateEmail('email', this.email)
      const passwordOk = this.validateRequired('password', this.password, 'Password')
      return emailOk && passwordOk
    },
    async signIn() {
      if (!this.validate()) {
        return
      }
      this.busy = true
      this.failure = ''
      try {
        await this.authenticate({ email: this.email, password: this.password })
        this.$router.push(this.$route.query.redirect || '/')
      } catch (error) {
        this.failure = 'Email or password is incorrect'
      } finally {
        this.busy = false
      }
    }
  }
}
</script>
