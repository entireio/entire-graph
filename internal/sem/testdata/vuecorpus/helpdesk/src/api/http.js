import axios from 'axios'

const TOKEN_KEY = 'helpdesk.token'

const http = axios.create({
  baseURL: process.env.VUE_APP_API_URL || '/api',
  timeout: 15000
})

export function getToken() {
  return window.localStorage.getItem(TOKEN_KEY)
}

export function setToken(token) {
  if (token) {
    window.localStorage.setItem(TOKEN_KEY, token)
  } else {
    window.localStorage.removeItem(TOKEN_KEY)
  }
}

export function attachToken(config) {
  const token = getToken()
  if (token) {
    config.headers.Authorization = 'Bearer ' + token
  }
  return config
}

function unwrap(response) {
  return response.data
}

http.interceptors.request.use(attachToken)

export function get(url, params) {
  return http.get(url, { params }).then(unwrap)
}

export function post(url, body) {
  return http.post(url, body).then(unwrap)
}

export function put(url, body) {
  return http.put(url, body).then(unwrap)
}

export function del(url) {
  return http.delete(url).then(unwrap)
}

export default http
