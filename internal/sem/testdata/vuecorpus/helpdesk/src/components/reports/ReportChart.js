import { formatDate } from '../../utils/date'

const BAR_STEP = 28

export default {
  name: 'ReportChart',
  props: {
    series: {
      type: Array,
      default: () => []
    },
    height: {
      type: Number,
      default: 240
    },
    metric: {
      type: String,
      default: 'count',
      validator: (value) => ['count', 'firstResponseMinutes'].indexOf(value) !== -1
    }
  },
  data() {
    return {
      hoverIndex: -1
    }
  },
  computed: {
    maxValue() {
      return this.series.reduce((max, point) => Math.max(max, point[this.metric]), 0) || 1
    },
    bars() {
      return this.series.map((point, index) => ({
        key: point.date,
        label: formatDate(point.date),
        height: this.barHeight(point[this.metric]),
        x: index * BAR_STEP
      }))
    }
  },
  methods: {
    barHeight(value) {
      return Math.round((value / this.maxValue) * this.height)
    },
    onHover(index) {
      this.hoverIndex = index
      this.$emit('hover', this.series[index] || null)
    }
  }
}
