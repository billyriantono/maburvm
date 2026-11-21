import type { AddonKey, Plan } from "@/types/plan"

type Priced = Pick<Plan, "price_monthly" | "price_hourly" | "currency">

function money(value: number, currency: string, maxDigits?: number): string {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: maxDigits }).format(value)
  } catch {
    return `${value} ${currency}` // a code the browser does not know
  }
}

/** The plan's display price in words, or null when it has none. */
export function planPrice(p: Priced): string | null {
  if (!p.currency) return null
  const parts: string[] = []
  if (p.price_monthly != null) parts.push(`${money(p.price_monthly, p.currency)} a month`)
  // Hourly prices run to fractions of a cent.
  if (p.price_hourly != null) parts.push(`${money(p.price_hourly, p.currency, 4)} an hour`)
  return parts.length ? parts.join(" or ") : null
}

type Allowances = Pick<Plan, "max_snapshots" | "max_backups" | "max_extra_ipv4" | "max_extra_ipv6"> &
  Partial<Pick<Plan, "network_type" | "nat_ports" | "nat_hostnames" | "max_extra_disks" | "max_images" | "max_floating_ips">>

function count(n: number, one: string, many: string): string {
  return n === 0 ? `No ${many}` : `${n} ${n === 1 ? one : many}`
}

/** What each VM on the plan may keep, for the limits that are set. */
export function planAllowances(p: Allowances): string[] {
  const out: string[] = []
  if (p.network_type === "nat") {
    out.push(`Private address with ${p.nat_ports ?? 0} public ports`)
    out.push(count(p.nat_hostnames ?? 0, "website hostname", "website hostnames"))
  }
  if (p.max_snapshots != null) out.push(count(p.max_snapshots, "snapshot", "snapshots"))
  if (p.max_backups != null) out.push(count(p.max_backups, "backup", "backups"))
  if (p.max_extra_ipv4 != null) out.push(count(p.max_extra_ipv4, "extra IPv4 address", "extra IPv4 addresses"))
  if (p.max_extra_ipv6 === 0) out.push("No IPv6 added later")
  if (p.max_extra_disks != null) out.push(count(p.max_extra_disks, "extra disk", "extra disks"))
  if (p.max_images != null) out.push(count(p.max_images, "saved image", "saved images"))
  if (p.max_floating_ips != null) out.push(count(p.max_floating_ips, "floating IP", "floating IPs"))
  return out
}

const addonWords: { key: AddonKey; label: string }[] = [
  { key: "snapshot", label: "Snapshot" },
  { key: "backup", label: "Backup" },
  { key: "image", label: "Saved image" },
  { key: "extra_disk_gb", label: "Extra disk, per GB" },
  { key: "floating_ip", label: "Floating IP" },
  { key: "extra_ipv4", label: "Extra IPv4 address" },
  { key: "extra_ipv6", label: "IPv6" },
]

/** The extras' monthly prices, in the order they are shown; empty when none are set. */
export function addonPrices(p: Pick<Plan, "addon_prices" | "currency">): { key: AddonKey; label: string; price: string }[] {
  if (!p.currency || !p.addon_prices) return []
  return addonWords
    .filter((a) => p.addon_prices?.[a.key] != null)
    .map((a) => ({ ...a, price: `${money(p.addon_prices![a.key]!, p.currency!)} a month` }))
}
