import { describe, expect, it } from 'vitest'
import { cephHealth } from './ceph'
import type { CephClusterNode } from '../types/storage'

const node = (status: CephClusterNode['status']): CephClusterNode =>
  ({ node_id: status, node_name: status, status, status_note: '', checked_at: null })

describe('cephHealth', () => {
  it('words the health a reachable cluster reports', () => {
    expect(cephHealth({ health: 'HEALTH_OK', nodes: [node('ok')] }).word).toBe('Healthy')
    expect(cephHealth({ health: 'HEALTH_WARN', nodes: [node('ok')] }).word).toBe('Warning')
    expect(cephHealth({ health: 'HEALTH_ERR', nodes: [node('unreachable'), node('degraded')] }).word).toBe('Error')
  })

  it('does not show a stale healthy state when no node reaches the cluster', () => {
    expect(cephHealth({ health: 'HEALTH_OK', nodes: [node('unreachable'), node('mismatch')] }).word).toBe('Unreachable')
  })

  it('says so when the cluster is on no node or was never read', () => {
    expect(cephHealth({ health: 'HEALTH_OK', nodes: [] }).word).toBe('Not enabled on a node')
    expect(cephHealth({ health: '', nodes: [node('ok')] }).word).toBe('Not checked yet')
  })
})
