import { describe, expect, it } from 'vitest'
import {
  accessLabel,
  endpoints,
  fields,
  inlineParts,
  markdownBlocks,
  matches,
  resolve,
  typeLabel,
  type OpenAPIDoc,
} from './openapi'

const doc: OpenAPIDoc = {
  info: { title: 'T', version: '1', description: '' },
  tags: [{ name: 'VMs' }, { name: 'Health' }],
  paths: {
    '/health': { get: { operationId: 'h', summary: 'Liveness', tags: ['Health'], responses: {}, 'x-access': 'public' } },
    '/api/v1/vms/{id}': {
      delete: { operationId: 'd', summary: 'Delete', tags: ['VMs'], responses: {}, 'x-access': 'vm:read vm:delete', 'x-customer': true },
      get: { operationId: 'g', summary: 'Get a VM', tags: ['VMs'], responses: {}, 'x-access': 'vm:read' },
    },
  },
  components: {
    schemas: { VM: { type: 'object', properties: { id: { type: 'string', format: 'uuid' }, tags: { type: 'array', items: { type: 'string' } } } } },
    responses: {},
  },
}

describe('openapi', () => {
  it('orders endpoints by tag, path and method', () => {
    expect(endpoints(doc).map((e) => `${e.method} ${e.path}`)).toEqual([
      'GET /api/v1/vms/{id}',
      'DELETE /api/v1/vms/{id}',
      'GET /health',
    ])
  })

  it('names types and follows references', () => {
    expect(typeLabel({ $ref: '#/components/schemas/VM' })).toBe('VM')
    expect(typeLabel({ type: ['string', 'null'], format: 'date-time' })).toBe('string (date-time) or null')
    expect(typeLabel({ type: 'array', items: { $ref: '#/components/schemas/VM' } })).toBe('list of VM')
    expect(typeLabel({ type: 'object', additionalProperties: { type: 'integer' } })).toBe('map of integer')
    expect(typeLabel({})).toBe('any')
    expect(resolve(doc, { $ref: '#/components/schemas/VM' }).type).toBe('object')
    expect(fields(doc, { type: 'array', items: { $ref: '#/components/schemas/VM' } }).map((f) => f.name)).toEqual(['id', 'tags'])
  })

  it('says who may call an endpoint', () => {
    const [get, del, health] = endpoints(doc)
    expect(accessLabel(get)).toBe('vm:read')
    expect(accessLabel(del)).toBe('vm:read + vm:delete · customers too')
    expect(accessLabel(health)).toBe('Public')
    expect(accessLabel({ ...get, 'x-access': 'vm:update|billing:update' })).toBe('vm:update or billing:update')
  })

  it('filters on every word', () => {
    const [get] = endpoints(doc)
    expect(matches(get, 'get vms')).toBe(true)
    expect(matches(get, 'vm:read')).toBe(true)
    expect(matches(get, 'delete')).toBe(false)
  })

  it('reads the Markdown the panel writes', () => {
    const blocks = markdownBlocks('## Head\n\nOne\nline.\n\n- a\n- b\n\n| x | y |\n|---|---|\n| 1 | 2 |')
    expect(blocks).toEqual([
      { kind: 'heading', text: 'Head' },
      { kind: 'paragraph', text: 'One line.' },
      { kind: 'list', items: ['a', 'b'] },
      { kind: 'table', head: ['x', 'y'], rows: [['1', '2']] },
    ])
    expect(inlineParts('Send `X-API-Key` **now**.')).toEqual([
      { text: 'Send ' },
      { text: 'X-API-Key', code: true },
      { text: ' ' },
      { text: 'now', bold: true },
      { text: '.' },
    ])
  })
})
