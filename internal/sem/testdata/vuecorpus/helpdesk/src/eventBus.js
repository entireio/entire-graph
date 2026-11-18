import Vue from 'vue'

// A bare Vue instance used as an application-wide event bus. Channels in use:
//   ticket:assigned   { ticketId, agentId }   an agent took ownership of a ticket
//   ticket:updated    { ticketId }            status, priority or tags changed
//   sla:breached      ticketId                a ticket passed its SLA deadline
//   reply:posted      { ticketId, reply }     a reply was added to a thread
export const EventBus = new Vue()

export default EventBus
