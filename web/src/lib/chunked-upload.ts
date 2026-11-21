// chunkRanges splits a file of size bytes into [start, end) ranges of at most
// chunk bytes, in order. An empty file has none.
export function chunkRanges(size: number, chunk: number): Array<[number, number]> {
  if (size <= 0) return []
  if (!Number.isFinite(chunk) || chunk <= 0) throw new Error('chunk size must be positive')
  const out: Array<[number, number]> = []
  for (let start = 0; start < size; start += chunk) {
    out.push([start, Math.min(start + chunk, size)])
  }
  return out
}

// uploadChunks sends each range in turn, trying a failed one again twice
// (the panel takes a chunk at the offset it has reached, so a repeat of one
// that did arrive is refused and the next is sent).
export async function uploadChunks(
  size: number,
  chunk: number,
  send: (start: number, end: number) => Promise<void>,
  attempts = 3,
): Promise<void> {
  for (const [start, end] of chunkRanges(size, chunk)) {
    let lastError: unknown
    for (let i = 0; i < attempts; i++) {
      try {
        await send(start, end)
        lastError = undefined
        break
      } catch (err) {
        lastError = err
      }
    }
    if (lastError !== undefined) throw lastError
  }
}

// arrivedOffset is where the panel says an upload has got to, from the 409 it
// answers a chunk at the wrong offset; undefined for any other failure.
export function arrivedOffset(err: unknown): number | undefined {
  const res = (err as { response?: { status?: number; data?: { data?: { offset?: unknown } } } })?.response
  const off = res?.data?.data?.offset
  return res?.status === 409 && typeof off === 'number' ? off : undefined
}
