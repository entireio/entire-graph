const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export function isBlank(value) {
  return value === null || value === undefined || String(value).trim() === ''
}

export function isEmail(value) {
  return !isBlank(value) && EMAIL_PATTERN.test(String(value).trim())
}

export function exceedsLength(value, max) {
  return !isBlank(value) && String(value).length > max
}
