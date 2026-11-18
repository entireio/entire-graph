import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { fetchBoard, fetchMembers, fetchProjects } from '@/api/projects'
import type { Board, Project } from '@/types/project'
import type { User } from '@/types/user'

export const useProjectStore = defineStore('projects', () => {
  const projects = ref<Project[]>([])
  const currentProjectId = ref<string | null>(null)
  const board = ref<Board | null>(null)
  const members = ref<User[]>([])

  const currentProject = computed(() => projects.value.find((project) => project.id === currentProjectId.value) ?? null)
  const activeProjects = computed(() => projects.value.filter((project) => !project.archived))

  async function loadProjects() {
    projects.value = await fetchProjects()
  }

  async function selectProject(projectId: string) {
    currentProjectId.value = projectId
    const project = currentProject.value
    if (!project) {
      return
    }
    const [loadedBoard, loadedMembers] = await Promise.all([fetchBoard(project.boardId), fetchMembers(projectId)])
    board.value = loadedBoard
    members.value = loadedMembers
  }

  function memberById(userId: string): User | undefined {
    return members.value.find((member) => member.id === userId)
  }

  return { projects, currentProjectId, board, members, currentProject, activeProjects, loadProjects, selectProject, memberById }
})
