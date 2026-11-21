import type { Project } from "@/types/team"

/** Who a member reaches, in words: every server, or what they were given. */
export function reachLabel(m: { vm_ids: string[]; project_ids: string[] }, projects: Pick<Project, "id" | "name">[]): string {
  if (m.vm_ids.length) return m.vm_ids.length === 1 ? "1 server" : `${m.vm_ids.length} servers`
  if (m.project_ids.length) {
    const names = m.project_ids.map((id) => projects.find((p) => p.id === id)?.name).filter(Boolean)
    if (names.length === m.project_ids.length) return `Projects: ${names.join(", ")}`
    return m.project_ids.length === 1 ? "1 project" : `${m.project_ids.length} projects`
  }
  return "Every server"
}
