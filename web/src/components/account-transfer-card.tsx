"use client"

import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

interface AccountTransfer {
  reset_day: number | null
  pooled: boolean
}

// How an account's transfer is counted: its own reset day over its plans',
// and whether its servers share one pool of transfer.
export function AccountTransferCard({ userId, canUpdate }: { userId: string; canUpdate: boolean }) {
  const queryClient = useQueryClient()
  const key = ["users", userId, "transfer"]
  const endpoint = `/api/v1/users/${userId}/transfer`
  const { data } = useQuery<AccountTransfer>({
    queryKey: key,
    queryFn: async () => (await api.get<AccountTransfer>(endpoint)).data.data,
  })
  const [day, setDay] = useState("")
  const [pooled, setPooled] = useState(false)
  useEffect(() => {
    if (!data) return
    setDay(data.reset_day ? String(data.reset_day) : "")
    setPooled(data.pooled)
  }, [data])

  const dayNum = day.trim() === "" ? null : Number(day)
  const dayOk = dayNum === null || (Number.isInteger(dayNum) && dayNum >= 1 && dayNum <= 28)
  const changed = !!data && (dayNum !== (data.reset_day ?? null) || pooled !== data.pooled)
  const save = useMutation({
    mutationFn: async () => {
      await api.put(endpoint, { reset_day: dayNum, pooled })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: key })
      toast.success("Transfer settings saved")
    },
    onError: (e: Error) => toast.error(`Not saved: ${e.message}`),
  })
  if (!data) return null

  return (
    <section className="rounded-lg border bg-card p-6 text-card-foreground">
      <h2 className="text-base font-semibold">Transfer</h2>
      <form
        className="mt-3 space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (dayOk && changed) save.mutate()
        }}
      >
        <div className="space-y-1">
          <label htmlFor="acct-reset-day" className="block text-sm text-muted-foreground">
            Allowance starts again on day
          </label>
          <Input
            id="acct-reset-day"
            type="number"
            min={1}
            max={28}
            className="w-28 tabular-nums"
            placeholder="Plan's"
            value={day}
            disabled={!canUpdate}
            aria-invalid={!dayOk}
            onChange={(e) => setDay(e.target.value)}
          />
          <p className="text-xs text-muted-foreground">
            {dayOk
              ? "Empty follows each server's plan. A change starts a new period for the account's servers, for example to match its billing day."
              : "A day from 1 to 28, or empty."}
          </p>
        </div>
        <label className="flex items-start gap-3">
          <input
            type="checkbox"
            checked={pooled}
            disabled={!canUpdate}
            onChange={(e) => setPooled(e.target.checked)}
            className="w-4 h-4 mt-0.5 accent-primary"
          />
          <span className="text-sm">
            Pool the transfer of the account&apos;s servers
            <span className="block text-xs text-muted-foreground">
              Its servers share the sum of their allowances, and go over together. Servers without an allowance
              stay outside the pool.
            </span>
          </span>
        </label>
        {canUpdate && (
          <div className="flex justify-end">
            <Button type="submit" size="sm" disabled={!dayOk || !changed || save.isPending}>
              {save.isPending ? "Saving..." : "Save transfer settings"}
            </Button>
          </div>
        )}
      </form>
    </section>
  )
}
