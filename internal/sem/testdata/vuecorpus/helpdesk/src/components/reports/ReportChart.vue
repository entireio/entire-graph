<template>
  <svg class="report-chart" :height="height" :width="series.length * 28" role="img">
    <g
      v-for="(bar, index) in bars"
      :key="bar.key"
      @mouseenter="onHover(index)"
      @mouseleave="onHover(-1)"
    >
      <rect :x="bar.x" :y="height - bar.height" width="20" :height="bar.height" />
      <title>{{ bar.label }}</title>
    </g>
  </svg>
</template>

<script src="./ReportChart.js"></script>
