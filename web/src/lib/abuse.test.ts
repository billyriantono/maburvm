import { describe, expect, it } from 'vitest'
import { abuseActionsFor, policyProblem, releasable, sampleValue } from './abuse'
import type { AbusePolicy } from '@/types'

const policy = (): AbusePolicy => ({
  outbound_syn: { enabled: true, threshold: 50, action: 'alert' },
  outbound_pps: { enabled: true, threshold: 150000, action: 'alert' },
  inbound_syn: { enabled: true, threshold: 20000, action: 'alert' },
  inbound_pps: { enabled: true, threshold: 200000, action: 'alert' },
  sustain_samples: 2,
  null_route_minutes: 30,
  rate_limit_mbps: 10,
  notify_owner: true,
})

describe('abuse policy', () => {
  it('accepts the defaults', () => {
    expect(policyProblem(policy())).toBeNull()
  })
  it('never offers to cut off the victim of a flood', () => {
    expect(abuseActionsFor('inbound_pps')).not.toContain('quarantine')
    const p = policy()
    p.inbound_pps.action = 'quarantine'
    expect(policyProblem(p)).toMatch(/Packets in/)
  })
  it('refuses a zero threshold and out-of-range numbers', () => {
    const p = policy()
    p.outbound_syn.threshold = 0
    expect(policyProblem(p)).toMatch(/above zero/)
    const q = policy()
    q.null_route_minutes = 20000
    expect(policyProblem(q)).toMatch(/Null-route/)
  })
})

describe('abuse events', () => {
  it('reads the value of the event kind from a sample', () => {
    const s = { syn_out: 1, syn_in: 2, pps_out: 3, pps_in: 4 }
    expect(sampleValue('inbound_pps', s)).toBe(4)
    expect(sampleValue('outbound_syn', s)).toBe(1)
  })
  it('releases only an action in force', () => {
    expect(releasable({ action_state: 'active' })).toBe(true)
    expect(releasable({ action_state: 'expired' })).toBe(false)
  })
})
