import { get } from './http'
import { toIsoDate } from '../utils/date'

export function fetchVolumeReport(from, to) {
  return get('/reports/volume', { from: toIsoDate(from), to: toIsoDate(to) })
}

export function fetchSlaReport(from, to) {
  return get('/reports/sla', { from: toIsoDate(from), to: toIsoDate(to) })
}
