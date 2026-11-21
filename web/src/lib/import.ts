import type { ImportItem, ImportItemRef, ImportKind, ImportPlan } from "@/types/import"

export const KIND_ORDER: ImportKind[] = ["pool", "plan", "user", "vm"]

export const itemKey = (kind: string, key: string) => `${kind}|${key}`

/** Items an import can still do something with. */
export const actionable = (it: ImportItem) => it.action === "create" || it.action === "link" || it.action === "update"

/**
 * Reads pasted billing references: one VM per line, its source ID then the
 * service reference, split by a comma, tab or spaces. A first line that is a
 * header is skipped.
 */
export function parseBillingRefs(text: string): { refs: Record<string, string>; errors: string[] } {
  const refs: Record<string, string> = {}
  const errors: string[] = []
  text.split(/\r?\n/).forEach((raw, i) => {
    const line = raw.trim()
    if (!line || line.startsWith("#")) return
    const [id, ...rest] = line.split(/[,;\t ]+/).filter(Boolean)
    const ref = rest.join(" ").trim()
    if (!/^\d+$/.test(id ?? "")) {
      if (i > 0 || /\d/.test(id ?? "")) errors.push(`Line ${i + 1}: the first value must be the source VM ID, a number.`)
      return
    }
    if (!ref) errors.push(`Line ${i + 1}: VM ${id} has no billing reference after it.`)
    else if (ref.length > 255) errors.push(`Line ${i + 1}: the reference of VM ${id} is longer than 255 characters.`)
    else refs[id] = ref
  })
  return { refs, errors }
}

export function formatBillingRefs(refs: Record<string, string> | undefined): string {
  return Object.entries(refs ?? {})
    .sort(([a], [b]) => Number(a) - Number(b))
    .map(([id, ref]) => `${id},${ref}`)
    .join("\n")
}

/**
 * The chosen items in the order an import needs them (pools, plans, accounts,
 * VMs), cut into requests short enough to finish well inside a page request.
 * A pool can carry thousands of addresses, so each goes alone.
 */
export function importBatches(items: ImportItem[], chosen: (it: ImportItem) => boolean, size = 20): ImportItemRef[][] {
  const out: ImportItemRef[][] = []
  for (const kind of KIND_ORDER) {
    const refs = items.filter((it) => it.kind === kind && chosen(it)).map(({ kind, key }) => ({ kind, key }))
    const step = kind === "pool" ? 1 : size
    for (let i = 0; i < refs.length; i += step) out.push(refs.slice(i, i + step))
  }
  return out
}

function csvCell(v: string): string {
  // A cell starting like a formula would run as one in a spreadsheet.
  const safe = /^[=+\-@\t\r]/.test(v) ? `'${v}` : v
  return /[",\n]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe
}

/**
 * The imported VMs and where each now lives, for updating the billing
 * system's services: its VM ID and owner's account ID here.
 */
export function billingLinksCsv(plan: ImportPlan): string {
  const owners = new Map(plan.items.filter((it) => it.kind === "user").map((it) => [it.key, it]))
  const rows = [["source_vm_id", "hostname", "vm_id", "owner_email", "owner_user_id", "billing_ref"]]
  for (const it of plan.items) {
    if (it.kind !== "vm" || it.action !== "exists" || !it.vm) continue
    const owner = owners.get(it.vm.owner_key)
    rows.push([it.key, it.vm.hostname, it.target_id ?? "", owner?.target_label ?? it.vm.owner_label, owner?.target_id ?? "", it.vm.billing_ref ?? ""])
  }
  return rows.map((r) => r.map(csvCell).join(",")).join("\n") + "\n"
}
