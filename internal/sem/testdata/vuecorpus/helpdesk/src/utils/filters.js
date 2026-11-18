import { formatDate, timeAgo } from './date'
import { priorityLabel } from './priority'

export function truncate(value, length) {
  if (!value || value.length <= length) {
    return value
  }
  return value.slice(0, length - 1) + '...'
}

export function registerFilters(Vue) {
  Vue.filter('date', formatDate)
  Vue.filter('timeAgo', timeAgo)
  Vue.filter('priority', priorityLabel)
  Vue.filter('truncate', truncate)
}
