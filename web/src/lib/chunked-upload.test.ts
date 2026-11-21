import { describe, expect, it } from 'vitest'
import { arrivedOffset, chunkRanges, uploadChunks } from './chunked-upload'

describe('chunkRanges', () => {
  it('splits a file into ordered ranges covering it once', () => {
    expect(chunkRanges(10, 4)).toEqual([[0, 4], [4, 8], [8, 10]])
    expect(chunkRanges(8, 4)).toEqual([[0, 4], [4, 8]])
    expect(chunkRanges(0, 4)).toEqual([])
  })
  it('refuses a chunk size that cannot advance', () => {
    expect(() => chunkRanges(10, 0)).toThrow()
  })
})

describe('uploadChunks', () => {
  it('sends every range in order and retries a failed one', async () => {
    const sent: number[] = []
    let failures = 1
    await uploadChunks(10, 4, async (start) => {
      if (start === 4 && failures-- > 0) throw new Error('flaky')
      sent.push(start)
    })
    expect(sent).toEqual([0, 4, 8])
  })
  it('gives up after its attempts', async () => {
    await expect(uploadChunks(4, 4, async () => { throw new Error('down') }, 2)).rejects.toThrow('down')
  })
})

describe('arrivedOffset', () => {
  it('reads the offset from a 409 only', () => {
    expect(arrivedOffset({ response: { status: 409, data: { data: { offset: 8 } } } })).toBe(8)
    expect(arrivedOffset({ response: { status: 500, data: { data: { offset: 8 } } } })).toBeUndefined()
    expect(arrivedOffset(new Error('x'))).toBeUndefined()
  })
})
