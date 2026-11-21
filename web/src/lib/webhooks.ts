// Words for an outbound webhook's subscription, from the panel's event catalog.

export interface EventCatalogGroup {
  group: string
  events: { event: string; label: string }[]
}

/** "Every event", or how many events and which, by their labels. */
export function summarizeEvents(events: string[], catalog: EventCatalogGroup[]): string {
  if (events.includes('*')) return 'Every event'
  const labels = new Map(catalog.flatMap((g) => g.events.map((e) => [e.event, e.label] as const)))
  const named = events.map((e) => labels.get(e) ?? e)
  if (named.length === 0) return 'No events'
  const list = named.length > 3 ? `${named.slice(0, 3).join('; ')}; and ${named.length - 3} more` : named.join('; ')
  return `${named.length} ${named.length === 1 ? 'event' : 'events'}: ${list}`
}

/** A signing secret long enough for the panel (32 characters or more). */
export function makeWebhookSecret(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(24)), (b) => b.toString(16).padStart(2, '0')).join('')
}
