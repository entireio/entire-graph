export const PRIORITIES = ['urgent', 'high', 'normal', 'low']

const LABELS = {
  urgent: 'Urgent',
  high: 'High',
  normal: 'Normal',
  low: 'Low'
}

const COLORS = {
  urgent: 'red',
  high: 'orange',
  normal: 'blue',
  low: 'grey'
}

export function isPriority(value) {
  return PRIORITIES.indexOf(value) !== -1
}

export function priorityLabel(priority) {
  return LABELS[priority] || 'Unknown'
}

export function priorityColor(priority) {
  return COLORS[priority] || 'grey'
}

export function comparePriority(a, b) {
  return PRIORITIES.indexOf(a.priority) - PRIORITIES.indexOf(b.priority)
}
