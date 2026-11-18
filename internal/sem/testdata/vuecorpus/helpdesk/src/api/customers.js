import { get, put } from './http'

export function fetchCustomers(params) {
  return get('/customers', params)
}

export function fetchCustomer(id) {
  return get('/customers/' + id)
}

export function updateCustomer(id, changes) {
  return put('/customers/' + id, changes)
}

export function fetchCustomerTickets(id) {
  return get('/customers/' + id + '/tickets')
}
