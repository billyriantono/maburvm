// Reading the panel's OpenAPI document (/api/v1/openapi.json) for the API
// reference page. Only the parts that document uses are typed.

export interface Schema {
  $ref?: string
  type?: string | string[]
  format?: string
  description?: string
  properties?: Record<string, Schema>
  items?: Schema
  additionalProperties?: Schema | boolean
  anyOf?: Schema[]
  enum?: unknown[]
  const?: unknown
  contentEncoding?: string
}

export interface Parameter {
  name: string
  in: string
  required?: boolean
  description?: string
  schema?: Schema
}

interface Content {
  [mime: string]: { schema?: Schema }
}

export interface Answer {
  $ref?: string
  description?: string
  content?: Content
}

export interface Operation {
  operationId: string
  summary: string
  description?: string
  tags: string[]
  parameters?: Parameter[]
  requestBody?: { content?: Content }
  responses: Record<string, Answer>
  'x-access'?: string
  'x-customer'?: boolean
}

export interface Endpoint extends Operation {
  method: string
  path: string
}

export interface OpenAPIDoc {
  info: { title: string; version: string; description: string }
  tags: { name: string; description?: string }[]
  paths: Record<string, Record<string, Operation>>
  webhooks?: Record<string, Record<string, Operation>>
  components: { schemas: Record<string, Schema>; responses: Record<string, Answer> }
}

const METHOD_ORDER = ['get', 'post', 'put', 'patch', 'delete']

// endpoints lists every operation, grouped in the document's tag order, then
// by path and by method.
export function endpoints(doc: OpenAPIDoc): Endpoint[] {
  const out: Endpoint[] = []
  for (const [path, item] of Object.entries(doc.paths)) {
    for (const [method, op] of Object.entries(item)) {
      out.push({ ...op, method: method.toUpperCase(), path })
    }
  }
  const tagIndex = new Map(doc.tags.map((t, i) => [t.name, i]))
  const rank = (e: Endpoint) => tagIndex.get(e.tags[0]) ?? doc.tags.length
  return out.sort(
    (a, b) =>
      rank(a) - rank(b) ||
      a.path.localeCompare(b.path) ||
      METHOD_ORDER.indexOf(a.method.toLowerCase()) - METHOD_ORDER.indexOf(b.method.toLowerCase()),
  )
}

const refName = (ref: string) => ref.slice(ref.lastIndexOf('/') + 1)

// resolve follows a component reference.
export function resolve(doc: OpenAPIDoc, s: Schema | undefined): Schema {
  if (!s) return {}
  if (s.$ref) return doc.components.schemas[refName(s.$ref)] ?? {}
  return s
}

export function resolveAnswer(doc: OpenAPIDoc, a: Answer): Answer {
  return a.$ref ? (doc.components.responses[refName(a.$ref)] ?? {}) : a
}

// typeLabel names a schema's type in a few words: "string (uuid)",
// "list of VMListItem", "VMListItem or null".
export function typeLabel(s: Schema | undefined): string {
  if (!s || Object.keys(s).length === 0) return 'any'
  if (s.$ref) return refName(s.$ref)
  if (s.anyOf) return s.anyOf.map(typeLabel).join(' or ')
  if (s.const !== undefined) return JSON.stringify(s.const)
  if (s.enum) return s.enum.map((v) => JSON.stringify(v)).join(' | ')
  const types = Array.isArray(s.type) ? s.type : s.type ? [s.type] : []
  const words = types.map((t) => {
    if (t === 'array') return `list of ${typeLabel(s.items)}`
    if (t === 'object' && s.additionalProperties && typeof s.additionalProperties === 'object' && !s.properties) {
      return `map of ${typeLabel(s.additionalProperties)}`
    }
    if (s.format && t !== 'null') return `${t} (${s.format})`
    return t
  })
  return words.join(' or ') || 'any'
}

export interface Field {
  name: string
  schema: Schema
}

// fields lists an object's properties, following references and lists of
// objects, so the page can show what is inside.
export function fields(doc: OpenAPIDoc, s: Schema | undefined): Field[] {
  let r = resolve(doc, s)
  if (r.anyOf) r = resolve(doc, r.anyOf.find((x) => x.type !== 'null'))
  if (r.type === 'array' || (Array.isArray(r.type) && r.type.includes('array'))) return fields(doc, r.items)
  return Object.entries(r.properties ?? {}).map(([name, schema]) => ({ name, schema }))
}

// accessLabel says in a few words who may call an endpoint.
export function accessLabel(op: Operation): string {
  const special: Record<string, string> = {
    public: 'Public',
    'signed-in': 'Any account',
    staff: 'Any staff',
    owner: 'Owner only',
    token: 'Node enrollment token',
    'api-key': 'API key',
    webhook: 'Billing signature',
    metrics: 'Metrics token',
  }
  const access = op['x-access'] ?? ''
  const who =
    special[access] ??
    access
      .split(/\s+/)
      .filter(Boolean)
      .map((term) => term.split('|').join(' or '))
      .join(' + ')
  return op['x-customer'] ? `${who} · customers too` : who
}

// matches reports whether an endpoint fits a search: every word must appear
// in its method, path, summary, tag or access.
export function matches(e: Endpoint, query: string): boolean {
  const hay = `${e.method} ${e.path} ${e.summary} ${e.tags.join(' ')} ${e['x-access'] ?? ''}`.toLowerCase()
  return query
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((w) => hay.includes(w))
}

export function slug(text: string): string {
  return text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

// The document's prose is Markdown. The reference renders the small part of it
// the panel writes: headings, paragraphs, lists, tables, `code` and **bold**.
export type Block =
  | { kind: 'heading'; text: string }
  | { kind: 'paragraph'; text: string }
  | { kind: 'list'; items: string[] }
  | { kind: 'table'; head: string[]; rows: string[][] }

const cells = (line: string) =>
  line
    .trim()
    .replace(/^\||\|$/g, '')
    .split('|')
    .map((c) => c.trim())

export function markdownBlocks(text: string): Block[] {
  const blocks: Block[] = []
  const lines = text.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (!line.trim()) continue
    if (line.startsWith('#')) {
      blocks.push({ kind: 'heading', text: line.replace(/^#+\s*/, '') })
    } else if (/^\s*[-*] /.test(line)) {
      const items: string[] = []
      while (i < lines.length && /^\s*[-*] /.test(lines[i])) items.push(lines[i++].replace(/^\s*[-*] /, ''))
      i--
      blocks.push({ kind: 'list', items })
    } else if (line.trim().startsWith('|')) {
      const rows: string[][] = []
      while (i < lines.length && lines[i].trim().startsWith('|')) {
        if (!/^\s*\|[\s|:-]+\|\s*$/.test(lines[i])) rows.push(cells(lines[i]))
        i++
      }
      i--
      blocks.push({ kind: 'table', head: rows[0] ?? [], rows: rows.slice(1) })
    } else {
      const para: string[] = []
      while (i < lines.length && lines[i].trim() && !/^(#|\s*[-*] |\s*\|)/.test(lines[i])) para.push(lines[i++])
      i--
      blocks.push({ kind: 'paragraph', text: para.join(' ') })
    }
  }
  return blocks
}

export type Inline = { text: string; code?: boolean; bold?: boolean }

export function inlineParts(text: string): Inline[] {
  const out: Inline[] = []
  const re = /`([^`]+)`|\*\*([^*]+)\*\*/g
  let last = 0
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m.index > last) out.push({ text: text.slice(last, m.index) })
    out.push(m[1] !== undefined ? { text: m[1], code: true } : { text: m[2], bold: true })
    last = re.lastIndex
  }
  if (last < text.length) out.push({ text: text.slice(last) })
  return out
}
