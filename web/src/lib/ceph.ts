import type { CephCluster } from "@/types/storage"

// cephHealth words a cluster's state for its badge. A cluster no node reaches
// is "Unreachable" whatever health was last read.
export function cephHealth(c: Pick<CephCluster, "health" | "nodes">) {
  const ok = "bg-emerald-50 text-emerald-800 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-200 dark:border-emerald-900"
  const warn = "bg-amber-50 text-amber-800 border-amber-200 dark:bg-amber-950 dark:text-amber-200 dark:border-amber-900"
  const bad = "bg-red-50 text-red-800 border-red-200 dark:bg-red-950 dark:text-red-200 dark:border-red-900"
  const idle = "bg-muted text-muted-foreground"
  if (c.nodes.length === 0) return { word: "Not enabled on a node", cls: idle }
  if (!c.nodes.some((n) => n.status === "ok" || n.status === "degraded")) return { word: "Unreachable", cls: bad }
  if (c.health === "HEALTH_OK") return { word: "Healthy", cls: ok }
  if (c.health === "HEALTH_WARN") return { word: "Warning", cls: warn }
  if (c.health === "HEALTH_ERR") return { word: "Error", cls: bad }
  return { word: "Not checked yet", cls: idle }
}
