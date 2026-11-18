<template>
  <div class="reports-view">
    <header class="page-header">
      <h1>Reports</h1>
      <form class="report-range" @submit.prevent="loadReports">
        <label for="report-from">From</label>
        <input id="report-from" v-model="from" type="date">
        <label for="report-to">To</label>
        <input id="report-to" v-model="to" type="date">
        <base-button type="submit" :busy="loading">Update</base-button>
      </form>
    </header>
    <div class="reports-view__summary">
      <stat-card label="Tickets" :value="totals.count" />
      <stat-card label="SLA met" :value="slaRate" tone="success" />
    </div>
    <report-chart :series="volume" metric="count" @hover="onChartHover" />
    <p v-if="hovered" class="reports-view__hover">
      {{ hovered.date | date }}: {{ hovered.count }} tickets
    </p>
  </div>
</template>

<script>
import { fetchVolumeReport, fetchSlaReport } from '@/api/reports'
import { addMinutes, toIsoDate } from '@/utils/date'
import StatCard from '@/components/reports/StatCard.vue'

const THIRTY_DAYS_IN_MINUTES = 30 * 24 * 60

export default {
  name: 'Reports',
  components: {
    StatCard,
    ReportChart: () => import(/* webpackChunkName: "charts" */ '@/components/reports/ReportChart.vue')
  },
  data() {
    const today = new Date()
    return {
      from: toIsoDate(addMinutes(today, -THIRTY_DAYS_IN_MINUTES)),
      to: toIsoDate(today),
      volume: [],
      sla: null,
      loading: false,
      hovered: null
    }
  },
  computed: {
    totals() {
      return { count: this.volume.reduce((sum, point) => sum + point.count, 0) }
    },
    slaRate() {
      if (!this.sla) {
        return '-'
      }
      return Math.round((this.sla.met / Math.max(1, this.sla.total)) * 100) + '%'
    }
  },
  mounted() {
    this.loadReports()
  },
  methods: {
    async loadReports() {
      this.loading = true
      try {
        const [volume, sla] = await Promise.all([
          fetchVolumeReport(this.from, this.to),
          fetchSlaReport(this.from, this.to)
        ])
        this.volume = volume
        this.sla = sla
      } finally {
        this.loading = false
      }
    },
    onChartHover(point) {
      this.hovered = point
    }
  }
}
</script>
