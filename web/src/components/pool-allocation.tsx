"use client"

import Link from "next/link"
import { useStoragePools } from "@/lib/hooks/use-storage"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import type { StoragePool } from "@/types"

// PoolAllocation says how much disk has been promised on a pool, next to how
// much it can hold. Disks are thin, so this is not the usage figure: a pool can
// be a third full and already promised twice over, and it is the promises that
// fill it later.
export function PoolAllocation({ pool }: { pool: StoragePool }) {
  const total = pool.total_space ?? 0
  const allocated = pool.allocated_space
  if (allocated === undefined || allocated === null) {
    return <p className="text-xs text-muted-foreground">Allocated to VM disks: not known.</p>
  }
  const limit = pool.allocation_limit ?? 0
  return (
    <div className="text-xs space-y-0.5">
      <div className="flex items-center justify-between gap-3">
        <span className="text-muted-foreground">Allocated to VM disks</span>
        <span className="font-medium">
          {formatBytes(allocated)}
          {total > 0 && <> of {formatBytes(total)} ({(allocated / total).toFixed(2)}x)</>}
        </span>
      </div>
      {total === 0 ? null : limit > 0 ? (
        allocated > limit ? (
          <p>
            <span className="font-medium text-red-600">Over the limit.</span>{" "}
            <span className="text-muted-foreground">
              The limit is {formatBytes(limit)} ({(limit / total).toFixed(2)}x). New disks are refused here.
            </span>
          </p>
        ) : (
          <p className="text-muted-foreground">
            Limit {formatBytes(limit)} ({(limit / total).toFixed(2)}x). {formatBytes(limit - allocated)} left to allocate.
          </p>
        )
      ) : (
        <p className="text-muted-foreground">
          No limit set.{" "}
          <Link href="/settings/system" className="underline underline-offset-2 text-foreground">
            Set a disk overcommit ratio
          </Link>{" "}
          under System settings, Quotas.
        </p>
      )}
    </div>
  )
}

// NodeStoragePools lists a node's pools on its own page. The node's disk gauge
// measures the system disk; VM disks usually live elsewhere, and this is where.
export function NodeStoragePools({ nodeId }: { nodeId: string }) {
  const { data: pools, isLoading, error } = useStoragePools()
  const mine = (pools ?? []).filter((p) => p.node_id === nodeId)

  return (
    <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
      <div className="flex items-baseline justify-between gap-4 mb-1">
        <h2 className="text-lg font-semibold text-foreground">Storage pools</h2>
        <Link href="/storage" className="text-sm underline underline-offset-2">Manage storage</Link>
      </div>
      <p className="text-sm text-muted-foreground mb-4">
        Where this node keeps VM disks. Used is what guests have written so far; allocated is the
        size the disks may grow to.
      </p>
      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading storage pools...</p>
      ) : error ? (
        <p className="text-sm text-muted-foreground">Storage pools could not be loaded: {error.message}</p>
      ) : mine.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No pool is registered for this node yet. One appears within a minute of the node coming
          online; until then new VMs go to the node&apos;s default directory.
        </p>
      ) : (
        <ul className="divide-y">
          {mine.map((pool) => (
            <li key={pool.id} className="py-3 first:pt-0 last:pb-0">
              <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 mb-1">
                <p className="text-sm font-medium">
                  {pool.name} <span className="font-mono font-normal text-muted-foreground">{pool.path}</span>
                  {pool.is_primary && <span className="ml-2 text-xs font-normal text-muted-foreground">New VMs go here</span>}
                </p>
                <p className="text-xs">
                  {pool.status === "online" ? (
                    <>
                      {formatBytes(pool.used_space ?? 0)} used of {formatBytes(pool.total_space ?? 0)},{" "}
                      {formatBytes(pool.available_space ?? 0)} free
                    </>
                  ) : (pool.total_space ?? 0) === 0 && pool.status === "offline" ? (
                    <span className="text-muted-foreground">Not measured yet</span>
                  ) : (
                    <span className="font-medium text-amber-600">
                      {pool.status === "offline" ? "Offline" : "Path missing on the node"}
                    </span>
                  )}
                </p>
              </div>
              <PoolAllocation pool={pool} />
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
