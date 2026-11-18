export function debounce(fn, wait) {
  let timer = null
  return function debounced(...args) {
    clearTimeout(timer)
    timer = setTimeout(() => fn.apply(this, args), wait)
  }
}
