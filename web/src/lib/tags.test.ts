import { describe, expect, it } from 'vitest'
import { parseTags } from './tags'

describe('parseTags', () => {
  it('splits on commas and spaces, lower-cases and drops repeats', () => {
    expect(parseTags(' Web, prod  db,web,, env:prod ')).toEqual(['web', 'prod', 'db', 'env:prod'])
  })
  it('reads an empty field as no tags', () => {
    expect(parseTags('  ,  ')).toEqual([])
  })
})
