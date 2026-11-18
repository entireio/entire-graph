<template>
  <ul id="task-list" :data-count="tasks.length">
    <li v-for="task in tasks" :key="task.id">{{ task.title }}</li>
  </ul>
  <button @click="reload">reload</button>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { fetchTasks, type Task } from './api'

const tasks = ref<Task[]>([])

function reload(): void {
  tasks.value = fetchTasks()
}

reload()
</script>
