import type { AbuseAction, AbuseActionState, AbuseEvent, AbuseKind, AbusePolicy } from '@/types'

export const ABUSE_KIND_WORDS: Record<AbuseKind, string> = {
  outbound_syn: 'New connections out',
  outbound_pps: 'Packets out',
  inbound_syn: 'New connections in',
  inbound_pps: 'Packets in',
  manual: 'Staff action',
}

export const ABUSE_ACTION_WORDS: Record<AbuseAction, string> = {
  alert: 'Alert staff only',
  rate_limit: 'Rate limit',
  quarantine: 'Quarantine',
  null_route: 'Null-route',
}

export const ABUSE_STATE_WORDS: Record<AbuseActionState, string> = {
  none: 'Watching',
  active: 'In force',
  released: 'Released',
  expired: 'Expired',
  failed: 'Failed',
}

/** A guest sending too much is slowed or cut off; one receiving too much is null-routed. */
export function abuseActionsFor(kind: AbuseKind): AbuseAction[] {
  return kind === 'inbound_syn' || kind === 'inbound_pps'
    ? ['alert', 'null_route']
    : ['alert', 'rate_limit', 'quarantine']
}

/** The unit a reading of this kind is measured in. */
export function abuseUnit(kind: AbuseKind): string {
  return kind === 'outbound_pps' || kind === 'inbound_pps' ? 'packets/s' : 'connections/s'
}

/** The reading of an event's kind in one evidence sample. */
export function sampleValue(kind: AbuseKind, s: { syn_out: number; syn_in: number; pps_out: number; pps_in: number }): number {
  switch (kind) {
    case 'outbound_syn':
      return s.syn_out
    case 'outbound_pps':
      return s.pps_out
    case 'inbound_syn':
      return s.syn_in
    case 'inbound_pps':
      return s.pps_in
    default:
      return 0
  }
}

/** Whether an event's action can be lifted now. */
export function releasable(e: Pick<AbuseEvent, 'action_state'>): boolean {
  return e.action_state === 'active'
}

/** Why a policy cannot be saved, or null. Mirrors the panel's own checks. */
export function policyProblem(p: AbusePolicy): string | null {
  for (const kind of ['outbound_syn', 'outbound_pps', 'inbound_syn', 'inbound_pps'] as const) {
    const r = p[kind]
    if (!(r.threshold > 0)) return `${ABUSE_KIND_WORDS[kind]}: the threshold must be above zero.`
    if (!abuseActionsFor(kind).includes(r.action)) return `${ABUSE_KIND_WORDS[kind]}: that action does not apply.`
  }
  if (!Number.isInteger(p.sustain_samples) || p.sustain_samples < 1 || p.sustain_samples > 30)
    return 'Readings over before acting: between 1 and 30.'
  if (!Number.isInteger(p.null_route_minutes) || p.null_route_minutes < 1 || p.null_route_minutes > 10080)
    return 'Null-route for: between 1 minute and a week (10080).'
  if (!Number.isInteger(p.rate_limit_mbps) || p.rate_limit_mbps < 1 || p.rate_limit_mbps > 100000)
    return 'Rate limit to: between 1 and 100000 Mbps.'
  return null
}
