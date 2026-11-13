import type { DashboardNode, DashboardOverview } from '@/lib/hooks/use-dashboard'

// What an operator should look at, worked out from what the panel has
// recorded. Shared by the dashboard and the monitoring page so both say the
// same thing.

function since(iso: string): string {
  const min = Math.floor((Date.now() - new Date(iso).getTime()) / 60_000)
  if (min < 60) return `${Math.max(min, 1)} min`
  const h = Math.floor(min / 60)
  return h < 48 ? `${h} h` : `${Math.floor(h / 24)} days`
}

export interface Attention { key: string; bad: boolean; text: string; href: string; go: string }

/** A node counts as silent when it is meant to be up but has sent nothing for 5 minutes. */
export function silentFor(n: DashboardNode): string | null {
  if (n.status !== 'active') return null
  if (!n.reported_at) return 'has never reported'
  return Date.now() - new Date(n.reported_at).getTime() > 5 * 60_000 ? `has not reported for ${since(n.reported_at)}` : null
}

/** Everything an operator should look at, most serious first. Built only from
 *  what the panel has recorded; an empty list means none of these is true. */
export function attentionList(o: DashboardOverview): Attention[] {
  const rows: Attention[] = []
  for (const n of o.nodes) {
    const href = `/nodes/${n.id}`
    if (n.status === 'offline') rows.push({ key: `off-${n.id}`, bad: true, text: `Node ${n.name} is offline${n.vm_count ? `, with ${n.vm_count} ${n.vm_count === 1 ? 'VM' : 'VMs'} on it` : ''}.`, href, go: 'Open node' })
    else if (n.status === 'maintenance') rows.push({ key: `mnt-${n.id}`, bad: false, text: `Node ${n.name} is in maintenance.`, href, go: 'Open node' })
    const silent = silentFor(n)
    if (silent) rows.push({ key: `sil-${n.id}`, bad: true, text: `Node ${n.name} ${silent}.`, href, go: 'Open node' })
    if (!silent && n.status === 'active' && n.memory_usage !== null && n.memory_usage >= 90)
      rows.push({ key: `mem-${n.id}`, bad: n.memory_usage >= 97, text: `Node ${n.name} is using ${Math.round(n.memory_usage)}% of its memory.`, href, go: 'Open node' })
    if (!silent && n.status === 'active' && n.cpu_usage !== null && n.cpu_usage >= 90)
      rows.push({ key: `cpu-${n.id}`, bad: false, text: `Node ${n.name}'s CPU is at ${Math.round(n.cpu_usage)}%.`, href, go: 'Open node' })
    if (n.conntrack_count && n.conntrack_max && n.conntrack_count / n.conntrack_max >= 0.8)
      rows.push({ key: `ct-${n.id}`, bad: true, text: `Node ${n.name}'s connection table is ${Math.round((n.conntrack_count / n.conntrack_max) * 100)}% full. A VM on it may be flooding.`, href: '/abuse', go: 'Open abuse' })
  }
  for (const v of o.vms_stuck)
    rows.push(v.status === 'error'
      ? { key: `err-${v.id}`, bad: true, text: `${v.hostname} failed ${since(v.updated_at)} ago.`, href: `/vms/${v.id}?tab=logs`, go: 'Open VM' }
      : { key: `stk-${v.id}`, bad: false, text: `${v.hostname} has been ${v.status} for ${since(v.updated_at)}.`, href: `/vms/${v.id}`, go: 'Open VM' })
  for (const v of o.vms_backup_failed)
    rows.push({ key: `bk-${v.id}`, bad: true, text: `A backup of ${v.hostname} failed ${since(v.updated_at)} ago.`, href: `/vms/${v.id}?tab=backups`, go: 'Open backups' })
  for (const s of o.storage_over_alert)
    rows.push({ key: `st-${s.node}-${s.name}`, bad: false, text: `Storage ${s.name} on ${s.node} is ${Math.round((s.used_bytes / s.total_bytes) * 100)}% full; its alert level is ${s.alert_threshold}%.`, href: '/storage', go: 'Open storage' })
  for (const p of o.address_pools) {
    const free = p.total - p.used
    if (p.total > 0 && p.used / p.total >= 0.9)
      rows.push({ key: `ip-${p.id}`, bad: free === 0, text: free === 0 ? `Address range ${p.name} is full. New VMs cannot get an address from it.` : `Address range ${p.name} has ${free} of ${p.total} addresses left.`, href: '/ip-pools', go: 'Open ranges' })
  }
  for (const v of o.vms_over_transfer)
    rows.push({ key: `tr-${v.id}`, bad: false, text: `${v.hostname} is over its monthly transfer limit.`, href: `/vms/${v.id}?tab=network`, go: 'Open VM' })
  if (o.failed_jobs_24h > 0)
    rows.push({ key: 'jobs', bad: false, text: `${o.failed_jobs_24h} background ${o.failed_jobs_24h === 1 ? 'job' : 'jobs'} gave up in the last 24 hours.`, href: '/settings/queue', go: 'Open job queue' })
  return rows.sort((a, b) => Number(b.bad) - Number(a.bad))
}
