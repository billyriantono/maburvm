"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { AlertTriangle, Loader2 } from "lucide-react"
import { Input } from "@/components/ui/input"
import {
  accessLabel,
  endpoints,
  fields,
  inlineParts,
  markdownBlocks,
  matches,
  resolveAnswer,
  slug,
  typeLabel,
  type Answer,
  type Endpoint,
  type OpenAPIDoc,
  type Schema,
} from "@/lib/openapi"

const SPEC = "/api/v1/openapi.json"

// The API reference, drawn from the panel's own OpenAPI document. Open to
// everyone (see middleware.ts): it describes the API's shape and holds no data.
export default function ApiReferencePage() {
  const [doc, setDoc] = useState<OpenAPIDoc | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState("")

  useEffect(() => {
    fetch(SPEC)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`the panel answered ${r.status}`))))
      .then(setDoc)
      .catch((e: Error) => setError(e.message))
  }, [])

  // A link to one endpoint or schema opens it, on load and on later clicks.
  useEffect(() => {
    if (!doc) return
    const open = () => {
      const el = window.location.hash && document.getElementById(decodeURIComponent(window.location.hash.slice(1)))
      if (!el) return
      if (el instanceof HTMLDetailsElement) el.open = true
      el.scrollIntoView()
    }
    open()
    window.addEventListener("hashchange", open)
    return () => window.removeEventListener("hashchange", open)
  }, [doc])

  const all = useMemo(() => (doc ? endpoints(doc) : []), [doc])
  const shown = useMemo(() => all.filter((e) => matches(e, query)), [all, query])
  const byTag = useMemo(() => {
    const groups = new Map<string, Endpoint[]>()
    for (const e of shown) groups.set(e.tags[0], [...(groups.get(e.tags[0]) ?? []), e])
    return groups
  }, [shown])

  if (error) {
    return (
      <main className="mx-auto max-w-2xl px-6 py-16 text-center">
        <AlertTriangle className="mx-auto h-10 w-10 text-destructive" aria-hidden="true" />
        <h1 className="mt-4 text-2xl font-semibold">The API reference could not be loaded</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          The panel was asked for <Mono>{SPEC}</Mono> and {error}. Reload to try again.
        </p>
      </main>
    )
  }
  if (!doc) {
    return (
      <main className="flex justify-center py-24">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" aria-label="Loading" />
      </main>
    )
  }

  const webhooks = Object.entries(doc.webhooks ?? {})
  const tags = doc.tags.filter((t) => byTag.has(t.name))

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 sm:px-6 lg:grid lg:grid-cols-[13rem_1fr] lg:gap-10">
      <nav aria-label="Sections" className="hidden lg:block">
        <div className="sticky top-8 space-y-1 text-sm">
          <a href="#overview" className="block py-1 text-muted-foreground hover:text-foreground">Overview</a>
          <a href="#errors" className="block py-1 text-muted-foreground hover:text-foreground">Errors</a>
          {tags.map((t) => (
            <a key={t.name} href={`#${slug(t.name)}`} className="block py-1 text-muted-foreground hover:text-foreground">
              {t.name} <span className="text-xs">({byTag.get(t.name)?.length})</span>
            </a>
          ))}
          {webhooks.length > 0 && (
            <a href="#webhooks" className="block py-1 text-muted-foreground hover:text-foreground">Webhooks</a>
          )}
          <a href="#schemas" className="block py-1 text-muted-foreground hover:text-foreground">Schemas</a>
        </div>
      </nav>

      <main className="min-w-0">
        <header className="mb-8">
          <p className="text-sm text-muted-foreground">
            <Link href="/" className="underline underline-offset-4 hover:text-foreground">Panel</Link> / API reference
          </p>
          <h1 className="mt-2 text-[2rem] font-semibold leading-tight tracking-[-0.01em]">
            {doc.info.title} <span className="text-base font-normal text-muted-foreground">version {doc.info.version}</span>
          </h1>
          <p className="mt-2 text-sm text-muted-foreground">
            {all.length} operations. The machine-readable document is at{" "}
            <a href={SPEC} className="font-mono text-foreground underline underline-offset-4">{SPEC}</a> (OpenAPI 3.1).
          </p>
        </header>

        <section id="overview" className="scroll-mt-8 rounded-md border bg-card p-6">
          <Prose text={doc.info.description} />
        </section>

        <section id="errors" className="mt-6 scroll-mt-8 rounded-md border bg-card p-6">
          <h2 className="text-lg font-semibold">Errors</h2>
          <dl className="mt-3 divide-y divide-[hsl(var(--hair))] text-sm">
            {(["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests", "Error"] as const).map((name) => {
              const a = doc.components.responses[name]
              if (!a) return null
              return (
                <div key={name} className="grid gap-1 py-2 sm:grid-cols-[10rem_1fr]">
                  <dt className="font-medium">{ERROR_CODES[name]}</dt>
                  <dd className="text-muted-foreground"><Inline text={a.description ?? ""} /></dd>
                </div>
              )
            })}
          </dl>
          <SchemaFields doc={doc} schema={doc.components.schemas.Error} />
        </section>

        <div className="sticky top-0 z-10 -mx-1 mt-8 bg-background px-1 py-3">
          <label htmlFor="api-search" className="sr-only">Filter operations</label>
          <Input
            id="api-search"
            type="search"
            placeholder="Filter by path, summary or permission, such as vms snapshot or vm:power"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query && (
            <p className="mt-2 text-xs text-muted-foreground" aria-live="polite">
              {shown.length} of {all.length} operations match.
            </p>
          )}
        </div>

        {tags.map((t) => (
          <section key={t.name} id={slug(t.name)} className="mt-8 scroll-mt-28">
            <h2 className="text-lg font-semibold">{t.name}</h2>
            {t.description && <p className="mt-1 text-sm text-muted-foreground">{t.description}</p>}
            <div className="mt-3 divide-y divide-[hsl(var(--hair))] rounded-md border bg-card">
              {byTag.get(t.name)?.map((e) => <EndpointRow key={e.operationId} doc={doc} e={e} />)}
            </div>
          </section>
        ))}
        {shown.length === 0 && (
          <p className="mt-8 text-sm text-muted-foreground">No operation matches “{query}”. Try fewer words.</p>
        )}

        {webhooks.length > 0 && !query && (
          <section id="webhooks" className="mt-10 scroll-mt-8">
            <h2 className="text-lg font-semibold">Webhooks</h2>
            <p className="mt-1 text-sm text-muted-foreground">Requests the panel sends to addresses you configure.</p>
            <div className="mt-3 divide-y divide-[hsl(var(--hair))] rounded-md border bg-card">
              {webhooks.map(([name, item]) => {
                const op = item.post
                return op ? <EndpointRow key={name} doc={doc} e={{ ...op, operationId: `webhook-${name}`, method: "POST", path: name }} /> : null
              })}
            </div>
          </section>
        )}

        {!query && <Schemas doc={doc} />}
      </main>
    </div>
  )
}

const ERROR_CODES: Record<string, string> = {
  BadRequest: "400",
  Unauthorized: "401",
  Forbidden: "403",
  NotFound: "404",
  TooManyRequests: "429",
  Error: "409, 5xx and others",
}

function EndpointRow({ doc, e }: { doc: OpenAPIDoc; e: Endpoint }) {
  const body = e.requestBody?.content?.["application/json"]?.schema
  const codes = Object.keys(e.responses)
  const success = codes.filter((c) => /^[23]/.test(c) || c === "405")
  const failures = codes.filter((c) => !success.includes(c))
  return (
    <details id={e.operationId} className="group scroll-mt-28">
      <summary className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-4 py-3 hover:bg-muted/50">
        <span className="w-14 shrink-0 font-mono text-xs font-semibold">{e.method}</span>
        <span className="min-w-0 break-all font-mono text-sm">{e.path}</span>
        <span className="text-sm text-muted-foreground">{e.summary}</span>
        <span className="ml-auto text-xs text-muted-foreground">{accessLabel(e)}</span>
      </summary>
      <div className="space-y-5 border-t border-[hsl(var(--hair))] px-4 py-4 text-sm">
        {e.description && <Prose text={e.description} />}
        {e.parameters && e.parameters.length > 0 && (
          <div>
            <h4 className="mb-2 font-medium">Parameters</h4>
            <Table
              head={["Name", "In", "Type", "Description"]}
              rows={e.parameters.map((p) => [
                <Mono key="n">{p.name}{p.required ? "" : "?"}</Mono>,
                p.in,
                <TypeName key="t" schema={p.schema} />,
                <Inline key="d" text={p.description ?? ""} />,
              ])}
            />
          </div>
        )}
        {body && (
          <div>
            <h4 className="mb-2 font-medium">Request body <span className="font-normal text-muted-foreground">(JSON, <TypeName schema={body} />)</span></h4>
            <SchemaFields doc={doc} schema={body} />
          </div>
        )}
        {success.map((code) => (
          <Response key={code} doc={doc} code={code} answer={resolveAnswer(doc, e.responses[code])} />
        ))}
        {failures.length > 0 && (
          <p className="text-muted-foreground">
            Errors: {failures.map((c) => (c === "default" ? "others" : c)).join(", ")}. See <a href="#errors" className="underline underline-offset-4">Errors</a>.
          </p>
        )}
      </div>
    </details>
  )
}

function Response({ doc, code, answer }: { doc: OpenAPIDoc; code: string; answer: Answer }) {
  const [mime, content] = Object.entries(answer.content ?? {})[0] ?? []
  return (
    <div>
      <h4 className="mb-2 font-medium">
        {code} {answer.description}
        {mime && mime !== "application/json" && <span className="font-normal text-muted-foreground"> ({mime})</span>}
      </h4>
      {content?.schema && mime === "application/json" && <SchemaFields doc={doc} schema={content.schema} />}
    </div>
  )
}

// SchemaFields shows an object's fields one level deep; nested objects are
// named and linked to their schema below.
function SchemaFields({ doc, schema }: { doc: OpenAPIDoc; schema: Schema | undefined }) {
  const list = fields(doc, schema)
  if (list.length === 0) {
    return <p className="text-muted-foreground"><TypeName schema={schema} /></p>
  }
  return (
    <Table
      head={["Field", "Type", "Description"]}
      rows={list.map((f) => {
        // An inline object (not a named schema) lists its field names here.
        const inner = f.schema.$ref || f.schema.items?.$ref ? [] : fields(doc, f.schema)
        return [
          <Mono key="n">{f.name}</Mono>,
          <span key="t">
            <TypeName schema={f.schema} />
            {inner.length > 0 && (
              <span className="block text-xs text-muted-foreground">{inner.map((sub) => sub.name).join(", ")}</span>
            )}
          </span>,
          <Inline key="d" text={f.schema.description ?? ""} />,
        ]
      })}
    />
  )
}

function TypeName({ schema }: { schema: Schema | undefined }) {
  const ref = schema?.$ref ?? (schema?.type === "array" ? schema.items?.$ref : undefined)
  if (!ref) return <span className="text-muted-foreground">{typeLabel(schema)}</span>
  const name = ref.slice(ref.lastIndexOf("/") + 1)
  return (
    <span className="text-muted-foreground">
      {schema?.type === "array" ? "list of " : ""}
      <a href={`#schema-${name}`} className="text-foreground underline underline-offset-4">{name}</a>
    </span>
  )
}

function Schemas({ doc }: { doc: OpenAPIDoc }) {
  const names = Object.keys(doc.components.schemas).sort()
  return (
    <section id="schemas" className="mt-10 scroll-mt-8">
      <h2 className="text-lg font-semibold">Schemas</h2>
      <p className="mt-1 text-sm text-muted-foreground">Objects the operations above send and receive, from the panel&apos;s own types.</p>
      <div className="mt-3 divide-y divide-[hsl(var(--hair))] rounded-md border bg-card">
        {names.map((name) => (
          <details key={name} id={`schema-${name}`} className="scroll-mt-28">
            <summary className="px-4 py-2 font-mono text-sm hover:bg-muted/50">{name}</summary>
            <div className="border-t border-[hsl(var(--hair))] px-4 py-3 text-sm">
              {doc.components.schemas[name].description && (
                <p className="mb-3 text-muted-foreground"><Inline text={doc.components.schemas[name].description ?? ""} /></p>
              )}
              <SchemaFields doc={doc} schema={doc.components.schemas[name]} />
            </div>
          </details>
        ))}
      </div>
    </section>
  )
}

function Table({ head, rows }: { head: string[]; rows: React.ReactNode[][] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead className="text-muted-foreground">
          <tr className="border-b">
            {head.map((h, i) => <th key={i} scope="col" className="py-1.5 pr-4 font-medium">{h}</th>)}
          </tr>
        </thead>
        <tbody className="divide-y divide-[hsl(var(--hair))]">
          {rows.map((cells, i) => (
            <tr key={i} className="align-top">
              {cells.map((c, j) => <td key={j} className="py-1.5 pr-4">{c}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function Mono({ children }: { children: React.ReactNode }) {
  return <code className="font-mono text-[0.8125rem]">{children}</code>
}

function Inline({ text }: { text: string }) {
  return (
    <>
      {inlineParts(text).map((p, i) =>
        p.code ? <Mono key={i}>{p.text}</Mono> : p.bold ? <strong key={i}>{p.text}</strong> : <span key={i}>{p.text}</span>,
      )}
    </>
  )
}

function Prose({ text }: { text: string }) {
  return (
    <div className="space-y-3 text-sm leading-relaxed">
      {markdownBlocks(text).map((b, i) => {
        switch (b.kind) {
          case "heading":
            return <h3 key={i} className="pt-2 text-base font-semibold first:pt-0"><Inline text={b.text} /></h3>
          case "list":
            return (
              <ul key={i} className="list-disc space-y-1 pl-5">
                {b.items.map((it, j) => <li key={j}><Inline text={it} /></li>)}
              </ul>
            )
          case "table":
            return (
              <Table
                key={i}
                head={b.head}
                rows={b.rows.map((r) => r.map((c, j) => <Inline key={j} text={c} />))}
              />
            )
          default:
            return <p key={i}><Inline text={b.text} /></p>
        }
      })}
    </div>
  )
}
