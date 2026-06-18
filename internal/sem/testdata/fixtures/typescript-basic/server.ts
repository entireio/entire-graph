import { readFileSync } from "fs"

export function loadConfig(): string {
  return readFileSync("config.json", "utf8")
}

export function handleRoute(): string {
  loadConfig()
  return "/users/{id}"
}
