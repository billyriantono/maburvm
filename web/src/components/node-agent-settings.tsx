"use client"

import { useQuery } from "@tanstack/react-query"
import { Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api-client"

// What GET /api/v1/nodes/:id/agent-settings returns (NodeAgentSettings in
// internal/panel/service/node_preflight.go).
interface AgentSettings {
  supported: boolean
  /** Variable name to value: "" not set, "set" for a secret, "x (default)". */
  settings: Record<string, string>
}

// NodeAgentSettings shows what the node's agent runs with, read from the node
// now: the variables in its systemd unit, secrets only as set or not.
export function NodeAgentSettings({ nodeId, online }: { nodeId: string; online: boolean }) {
  const { data, isLoading, isFetching, error, refetch } = useQuery<AgentSettings>({
    queryKey: ["nodes", "agent-settings", nodeId],
    queryFn: async () => (await api.get<AgentSettings>(`/api/v1/nodes/${nodeId}/agent-settings`)).data.data,
    enabled: online,
    retry: false,
    staleTime: 60_000,
  })
  const rows = Object.entries(data?.settings ?? {}).sort(([a], [b]) => a.localeCompare(b))

  return (
    <div className="mb-6 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
      <div className="mb-1 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-lg font-semibold text-foreground">Agent settings</h2>
        {online && (
          <Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>
            {isFetching && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
            {isFetching ? "Asking…" : "Ask again"}
          </Button>
        )}
      </div>
      <p className="mb-4 text-sm text-muted-foreground">
        What the agent on this node runs with, from its environment on the node (the systemd unit, or what
        install-agent.sh wrote). Secrets show only whether they are set. They are changed on the node: edit the unit
        with systemctl edit maburvm-agent and restart the agent.
      </p>

      {!online ? (
        <p className="text-sm text-muted-foreground">The node is not answering, so its agent cannot be asked.</p>
      ) : isLoading ? (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          Asking the node
        </p>
      ) : error ? (
        <p className="text-sm text-red-700 dark:text-red-400">The node did not answer: {(error as Error).message}</p>
      ) : !data?.supported ? (
        <p className="text-sm">
          <span className="font-medium">Not available on this node.</span>{" "}
          <span className="text-muted-foreground">Its agent is too old to report its settings. Update the agent (Settings, System) to see them.</span>
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-muted-foreground">
                <th className="py-2 pr-4 font-medium">Variable</th>
                <th className="py-2 font-medium">Value</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {rows.map(([name, value]) => (
                <tr key={name}>
                  <td className="py-2 pr-4 font-mono text-xs">{name}</td>
                  <td className="py-2 break-all">
                    {value === "" ? (
                      <span className="text-muted-foreground">Not set</span>
                    ) : value.endsWith(" (default)") ? (
                      <span className="text-muted-foreground">
                        <span className="font-mono text-xs">{value.slice(0, -" (default)".length)}</span> (default)
                      </span>
                    ) : value === "set" ? (
                      "Set"
                    ) : (
                      <span className="font-mono text-xs">{value}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
