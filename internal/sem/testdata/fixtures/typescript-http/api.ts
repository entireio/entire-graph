import axios from "axios"

// Server side: registers a route handler.
export function register(app: any): void {
  app.get("/api/health", health)
}

export function health(): string {
  return "ok"
}

// Client side: calls the same route. Both reference external:route:/api/health,
// so a consumer can link the client call to the handler.
export async function ping(): Promise<unknown> {
  return axios.get("/api/health")
}

export async function createUser(body: unknown): Promise<unknown> {
  return fetch("/api/users", { method: "POST", body: JSON.stringify(body) })
}
