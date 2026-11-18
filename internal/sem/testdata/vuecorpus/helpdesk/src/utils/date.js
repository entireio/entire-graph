const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

function pad(value) {
  return value < 10 ? '0' + value : String(value)
}

export function toDate(value) {
  return value instanceof Date ? value : new Date(value)
}

export function toIsoDate(value) {
  const date = toDate(value)
  return date.getFullYear() + '-' + pad(date.getMonth() + 1) + '-' + pad(date.getDate())
}

export function formatDate(value, withTime) {
  if (!value) {
    return ''
  }
  const date = toDate(value)
  const day = MONTHS[date.getMonth()] + ' ' + date.getDate() + ', ' + date.getFullYear()
  if (!withTime) {
    return day
  }
  return day + ' ' + pad(date.getHours()) + ':' + pad(date.getMinutes())
}

export function timeAgo(value, now) {
  const seconds = Math.round(((now || Date.now()) - toDate(value).getTime()) / 1000)
  if (seconds < 60) {
    return 'just now'
  }
  if (seconds < 3600) {
    return Math.floor(seconds / 60) + 'm ago'
  }
  if (seconds < 86400) {
    return Math.floor(seconds / 3600) + 'h ago'
  }
  return formatDate(value)
}

export function addMinutes(value, minutes) {
  return new Date(toDate(value).getTime() + minutes * 60000)
}

export function minutesBetween(start, end) {
  return Math.round((toDate(end).getTime() - toDate(start).getTime()) / 60000)
}
