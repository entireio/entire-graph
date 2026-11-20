const DAY_MS = 24 * 60 * 60 * 1000

export function formatDate(value: string | null, locale = 'en-US'): string {
  if (!value) {
    return ''
  }
  return new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric' }).format(new Date(value))
}

export function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0].toUpperCase())
    .join('')
}

export function pluralize(count: number, singular: string, plural = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : plural}`
}

export function relativeDueLabel(dueDate: string | null, now: Date = new Date()): string {
  if (!dueDate) {
    return 'No due date'
  }
  const days = Math.round((new Date(dueDate).getTime() - now.getTime()) / DAY_MS)
  if (days === 0) {
    return 'Due today'
  }
  if (days < 0) {
    return `Overdue by ${pluralize(-days, 'day')}`
  }
  return `Due in ${pluralize(days, 'day')}`
}
