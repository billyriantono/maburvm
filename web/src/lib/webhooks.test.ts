import { describe, expect, it } from 'vitest'
import { makeWebhookSecret, summarizeEvents } from './webhooks'

const catalog = [
  { group: 'VMs', events: [{ event: 'vm.created', label: 'A VM finished installing' }, { event: 'vm.destroyed', label: 'A VM was deleted' }] },
  { group: 'Backups', events: [{ event: 'backup.failed', label: 'A backup failed for good' }, { event: 'backup.completed', label: 'A backup finished' }] },
]

describe('summarizeEvents', () => {
  it('says every event for the wildcard', () => {
    expect(summarizeEvents(['*'], catalog)).toBe('Every event')
    expect(summarizeEvents(['vm.created', '*'], catalog)).toBe('Every event')
  })

  it('names the events by their labels, and an unknown one by its name', () => {
    expect(summarizeEvents(['vm.created'], catalog)).toBe('1 event: A VM finished installing')
    expect(summarizeEvents(['backup.failed', 'user.renamed'], catalog)).toBe('2 events: A backup failed for good; user.renamed')
  })

  it('shortens a long list', () => {
    expect(summarizeEvents(['vm.created', 'vm.destroyed', 'backup.failed', 'backup.completed'], catalog)).toBe(
      '4 events: A VM finished installing; A VM was deleted; A backup failed for good; and 1 more',
    )
    expect(summarizeEvents([], catalog)).toBe('No events')
  })
})

describe('makeWebhookSecret', () => {
  it('makes a fresh secret the panel accepts', () => {
    const a = makeWebhookSecret()
    expect(a).toMatch(/^[0-9a-f]{48}$/)
    expect(makeWebhookSecret()).not.toBe(a)
  })
})
