"use client"

import { createContext, useContext, useState, type ReactNode } from "react"
import { useMemberships } from "@/lib/hooks/use-team"
import type { Membership } from "@/types/team"

// The account a customer page about an account's own resources (images,
// floating IPs, VPCs, firewall groups, SSH keys, recipes) works in: their own
// (undefined) or a team account. The resource hooks read it, so the pages and
// the components inside them need not pass it down.
const ActingAccount = createContext<{ id?: string; canChange: boolean }>({ canChange: true })

/** The acting account's id, undefined for the caller's own. */
export function useActingAccount(): string | undefined {
  return useContext(ActingAccount).id
}

/** Whether the caller may change the acting account's resources. */
export function useCanChangeResources(): boolean {
  return useContext(ActingAccount).canChange
}

/** Request options that name the acting account; none for the caller's own. */
export function accountParams(id?: string) {
  return id ? { params: { account_id: id } } : undefined
}

/** The team accounts whose resources a membership lets the caller see. */
export function resourceAccounts(memberships: Membership[] | undefined): Membership[] {
  return (memberships ?? []).filter((m) => m.can.includes("view"))
}

/**
 * Wraps a customer resource page: an account picker when the caller works in
 * team accounts, and the chosen account for the hooks inside.
 */
export function ActingAccountScope({ children, label = "Account" }: { children: ReactNode; label?: string }) {
  const { data } = useMemberships()
  const accounts = resourceAccounts(data?.memberships)
  const [id, setId] = useState("")
  const chosen = accounts.find((m) => m.account_id === id)
  const canChange = !chosen || chosen.can.includes("resources")
  return (
    <ActingAccount.Provider value={{ id: chosen?.account_id, canChange }}>
      {accounts.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <label htmlFor="acting-account" className="text-sm text-muted-foreground">{label}</label>
          <select id="acting-account" value={chosen ? id : ""} onChange={(e) => setId(e.target.value)}
            className="h-9 rounded-md border border-input bg-transparent px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            <option value="">Your own</option>
            {accounts.map((m) => <option key={m.account_id} value={m.account_id}>{m.account_name || m.account_email}</option>)}
          </select>
          {!canChange && <span className="text-sm text-muted-foreground">Your role there lets you look, not change.</span>}
        </div>
      )}
      {children}
    </ActingAccount.Provider>
  )
}
