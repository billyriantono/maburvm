"use client"

import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Loader2, RefreshCw } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import { useNodes } from "@/lib/hooks/use-nodes"
import { formatBytes } from "@/lib/hooks/use-bandwidth"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useConfirm } from "@/components/confirm-provider"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"

type Kind = "s3" | "sftp" | "path"

interface Destination {
  id: string
  name: string
  kind: Kind
  is_default: boolean
  endpoint?: string
  region?: string
  bucket?: string
  path_prefix?: string
  access_key?: string
  use_tls: boolean
  host?: string
  port?: number
  user?: string
  host_key?: string
  host_key_fingerprint?: string
  auth?: "password" | "key"
  path?: string
  has_secret: boolean
  /** New backups are encrypted on the node before they are uploaded. */
  encrypt: boolean
  encryption_key_id?: string
  /** Keys kept: older backups need the key they were made with. */
  encryption_keys: number
  backups: number
  backup_bytes: number
}

interface ExportedKey {
  id: string
  key: string
  created_at: string
  current: boolean
}

interface TestResult {
  node_id?: string
  name: string
  status: "ok" | "failed" | "needs_agent_update" | "unreachable" | "not_applicable"
  message?: string
}

const QUERY_KEY = ["settings", "backup-destinations"]
const ENDPOINT = "/api/v1/settings/system/backup-destinations"

const RESULT_WORDS: Record<TestResult["status"], { word: string; lamp: string }> = {
  ok: { word: "Works", lamp: "bg-success" },
  failed: { word: "Failed", lamp: "bg-destructive" },
  needs_agent_update: { word: "Needs an agent update", lamp: "bg-warning" },
  unreachable: { word: "Node not reached", lamp: "bg-destructive" },
  not_applicable: { word: "Not tested", lamp: "bg-muted-foreground" },
}

const KIND_WORDS: Record<Kind, string> = { s3: "S3 bucket", sftp: "SFTP server", path: "Directory on the nodes" }

function where(d: Destination): string {
  if (d.kind === "sftp") return `${d.user}@${d.host}${d.port && d.port !== 22 ? `:${d.port}` : ""}${d.path ? `, ${d.path}` : ""}`
  if (d.kind === "path") return d.path ?? ""
  return `${d.bucket}${d.endpoint ? ` at ${d.endpoint}` : ""}${d.path_prefix ? `, ${d.path_prefix}` : ""}`
}

interface Form {
  name: string
  kind: Kind
  endpoint: string
  region: string
  bucket: string
  path_prefix: string
  access_key: string
  secret_key: string
  use_tls: boolean
  host: string
  port: string
  user: string
  auth: "password" | "key"
  password: string
  private_key: string
  host_key: string
  path: string
}

function formFor(d?: Destination): Form {
  return {
    name: d?.name ?? "", kind: d?.kind ?? "s3",
    endpoint: d?.endpoint ?? "", region: d?.region ?? "", bucket: d?.bucket ?? "", path_prefix: d?.path_prefix ?? "",
    access_key: d?.access_key ?? "", secret_key: "", use_tls: d?.use_tls ?? true,
    host: d?.host ?? "", port: d?.port ? String(d.port) : "22", user: d?.user ?? "", auth: d?.auth ?? "password",
    password: "", private_key: "", host_key: d?.host_key ?? "", path: d?.path ?? "",
  }
}

function bodyOf(f: Form) {
  const common = { name: f.name.trim(), kind: f.kind }
  if (f.kind === "sftp") {
    return { ...common, host: f.host.trim(), port: Number(f.port) || 22, user: f.user.trim(), auth: f.auth,
      password: f.auth === "password" ? f.password : "", private_key: f.auth === "key" ? f.private_key : "",
      host_key: f.host_key.trim(), path: f.path.trim() }
  }
  if (f.kind === "path") return { ...common, path: f.path.trim() }
  return { ...common, endpoint: f.endpoint.trim(), region: f.region.trim(), bucket: f.bucket.trim(), path_prefix: f.path_prefix.trim(),
    access_key: f.access_key.trim(), secret_key: f.secret_key, use_tls: f.use_tls }
}

const fieldClass = "mb-2 block text-sm font-medium"

// Where backups are kept: S3 buckets, SFTP servers and directories mounted on
// the nodes. New backups go to the default; each backup remembers its own, so
// older ones stay restorable when the default changes. Secrets are write-only:
// the server stores them encrypted and only says whether one is saved.
export function BackupDestinationSettings() {
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const { data: list, isLoading, isError, error, refetch } = useQuery<Destination[]>({
    queryKey: QUERY_KEY,
    queryFn: async () => (await api.get<Destination[]>(ENDPOINT)).data.data,
  })
  const [editing, setEditing] = useState<Destination | "new" | null>(null)
  const refresh = () => queryClient.invalidateQueries({ queryKey: QUERY_KEY })

  const makeDefault = useMutation<void, Error, string>({
    mutationFn: async (id) => { await api.post(`${ENDPOINT}/${id}/default`) },
    onSuccess: () => { refresh(); toast.success("New backups go to this destination") },
    onError: (err) => toast.error(err.message),
  })
  const remove = useMutation<void, Error, string>({
    mutationFn: async (id) => { await api.delete(`${ENDPOINT}/${id}`) },
    onSuccess: () => { refresh(); toast.success("Backup destination removed") },
  })

  return (
    <Card>
      <CardHeader className="border-b">
        <CardTitle className="text-xl">Backup destinations</CardTitle>
        <CardDescription>
          Where backups are written and restored from. New backups go to the default; every backup stays in the destination
          it was made in. The nodes reach each destination directly, and the panel deletes from S3 buckets and SFTP servers.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5 p-6">
        {isLoading ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Loading the backup destinations
          </p>
        ) : isError ? (
          <div>
            <p className="text-sm font-medium text-destructive">Could not load the backup destinations</p>
            <p className="mt-1 text-sm text-muted-foreground">{(error as Error)?.message}</p>
            <Button type="button" variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>
              <RefreshCw className="h-4 w-4" aria-hidden="true" />
              Retry
            </Button>
          </div>
        ) : (
          <>
            {list && list.length > 0 ? (
              <ul className="divide-y divide-hair rounded-md border">
                {list.map((d) => (
                  <li key={d.id} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 p-4">
                    <div className="min-w-0">
                      <p className="font-medium">
                        {d.name}
                        {d.is_default && <span className="ml-2 text-xs font-normal text-muted-foreground">Default</span>}
                      </p>
                      <p className="break-words text-xs text-muted-foreground">{KIND_WORDS[d.kind]}: {where(d)}</p>
                      <p className="text-xs text-muted-foreground tabular-nums">
                        {d.backups} {d.backups === 1 ? "backup" : "backups"}, {formatBytes(d.backup_bytes)}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {d.encrypt
                          ? `New backups encrypted${d.encryption_keys > 1 ? `; ${d.encryption_keys} keys kept for older ones` : ""}`
                          : d.encryption_keys > 0 ? "New backups not encrypted; older keys kept" : "Backups stored as they are, not encrypted"}
                      </p>
                    </div>
                    <div className="flex flex-wrap gap-2">
                      <Button type="button" variant="outline" size="sm" onClick={() => setEditing(d)}>Edit</Button>
                      <EncryptionControls d={d} onChange={refresh} />
                      {!d.is_default && (
                        <Button type="button" variant="outline" size="sm" disabled={makeDefault.isPending} onClick={() => makeDefault.mutate(d.id)}>
                          Make default
                        </Button>
                      )}
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        className="text-destructive hover:text-destructive"
                        onClick={() =>
                          confirm({
                            title: `Remove ${d.name}?`,
                            description: d.is_default
                              ? "It is the default: with no destination left, new backups work only on nodes that have storage keys of their own."
                              : "Its settings are deleted from the panel. A destination that still holds backups cannot be removed.",
                            confirmLabel: "Remove",
                            destructive: true,
                            action: () => remove.mutateAsync(d.id),
                          })
                        }
                      >
                        Remove
                      </Button>
                    </div>
                  </li>
                ))}
              </ul>
            ) : (
              <p role="status" className="rounded-md border bg-muted px-3 py-2 text-sm">
                None set. A node can only back up with storage keys in its own environment file, and the panel cannot delete or
                move those backups.
              </p>
            )}
            {editing ? (
              <DestinationForm
                key={editing === "new" ? "new" : editing.id}
                saved={editing === "new" ? undefined : editing}
                onDone={() => { setEditing(null); refresh() }}
                onCancel={() => setEditing(null)}
              />
            ) : (
              <Button type="button" variant="outline" onClick={() => setEditing("new")}>Add destination</Button>
            )}
          </>
        )}
      </CardContent>
    </Card>
  )
}

// EncryptionControls turn encryption of new backups on and off, make a new key,
// and show the keys for safekeeping outside the panel: a backup encrypted with
// a key that is lost cannot be restored by anyone.
function EncryptionControls({ d, onChange }: { d: Destination; onChange: () => void }) {
  const confirm = useConfirm()
  const [keys, setKeys] = useState<ExportedKey[] | null>(null)
  const call = useMutation<void, Error, { path: string; body?: unknown; done: string }>({
    mutationFn: async ({ path, body }) => { await api.post(`${ENDPOINT}/${d.id}/${path}`, body) },
    onSuccess: (_, v) => { onChange(); toast.success(v.done) },
    onError: (err) => toast.error(err.message),
  })
  const reveal = useMutation<ExportedKey[], Error, void>({
    mutationFn: async () => (await api.post<ExportedKey[]>(`${ENDPOINT}/${d.id}/export-keys`)).data.data,
    onSuccess: setKeys,
    onError: (err) => toast.error(err.message),
  })
  return (
    <>
      <Button type="button" variant="outline" size="sm" disabled={call.isPending}
        onClick={() => d.encrypt
          ? confirm({
              title: `Stop encrypting backups in ${d.name}?`,
              description: "New backups are stored as they are. Encrypted ones keep their keys and still restore.",
              confirmLabel: "Stop encrypting",
              action: () => call.mutateAsync({ path: "encryption", body: { encrypt: false }, done: "New backups are no longer encrypted" }),
            })
          : confirm({
              title: `Encrypt new backups in ${d.name}?`,
              description: "Each node encrypts a backup before uploading it, with a key the panel keeps sealed. Export the keys and keep them away from the panel: without them, encrypted backups cannot be restored if the panel is lost. Nodes whose agent is older than the panel keep backing up unencrypted until updated.",
              confirmLabel: "Encrypt new backups",
              action: () => call.mutateAsync({ path: "encryption", body: { encrypt: true }, done: "New backups are encrypted" }),
            })}>
        {d.encrypt ? "Stop encrypting" : "Encrypt backups"}
      </Button>
      {d.encrypt && (
        <Button type="button" variant="outline" size="sm" disabled={call.isPending}
          onClick={() => confirm({
            title: "Encrypt new backups with a new key?",
            description: "Backups already made keep the key they were made with, which stays here. Export the keys again afterwards.",
            confirmLabel: "Make a new key",
            action: () => call.mutateAsync({ path: "rotate-key", done: "New backups use the new key" }),
          })}>
          New key
        </Button>
      )}
      {d.encryption_keys > 0 && (
        <Button type="button" variant="outline" size="sm" disabled={reveal.isPending} onClick={() => reveal.mutate()}>
          Export keys
        </Button>
      )}
      <Dialog open={keys !== null} onOpenChange={(open) => !open && setKeys(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Encryption keys of {d.name}</DialogTitle>
            <DialogDescription>
              Keep these somewhere the panel is not, such as a password manager. With them and the backup files,
              cmd/backup-decrypt turns a backup back into its disk image without the panel.
            </DialogDescription>
          </DialogHeader>
          <label htmlFor={`keys-${d.id}`} className="sr-only">Keys</label>
          <textarea id={`keys-${d.id}`} readOnly rows={8} className="w-full rounded-md border bg-muted p-3 font-mono text-xs"
            value={JSON.stringify(keys ?? [], null, 2)} onFocus={(e) => e.currentTarget.select()} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setKeys(null)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function DestinationForm({ saved, onDone, onCancel }: { saved?: Destination; onDone: () => void; onCancel: () => void }) {
  const { data: nodes } = useNodes()
  const [f, setF] = useState<Form>(() => formFor(saved))
  const [formError, setFormError] = useState<string | null>(null)
  const [skipped, setSkipped] = useState<Set<string>>(new Set())
  const [results, setResults] = useState<TestResult[] | null>(null)
  const [fingerprint, setFingerprint] = useState(saved?.host_key_fingerprint ?? "")
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((prev) => ({ ...prev, [k]: v }))

  const save = useMutation<void, Error, void>({
    mutationFn: async () => {
      if (saved) await api.put(`${ENDPOINT}/${saved.id}`, bodyOf(f))
      else await api.post(ENDPOINT, bodyOf(f))
    },
    onSuccess: () => { toast.success("Backup destination saved"); onDone() },
    onError: (err) => setFormError(err.message),
  })
  const test = useMutation<TestResult[], Error, void>({
    mutationFn: async () => {
      const node_ids = (nodes ?? []).filter((n) => !skipped.has(n.id)).map((n) => n.id)
      const res = await api.post<{ results: TestResult[] }>(`${ENDPOINT}/test`, { ...bodyOf(f), id: saved?.id, node_ids })
      return res.data.data.results
    },
    onMutate: () => setResults(null),
    onSuccess: (next) => { setFormError(null); setResults(next) },
    onError: (err) => setFormError(err.message),
  })
  const scan = useMutation<{ host_key: string; fingerprint: string }, Error, void>({
    mutationFn: async () => (await api.post<{ host_key: string; fingerprint: string }>(`${ENDPOINT}/scan-host-key`, { host: f.host.trim(), port: Number(f.port) || 22 })).data.data,
    onSuccess: (k) => { set("host_key", k.host_key); setFingerprint(k.fingerprint); setFormError(null) },
    onError: (err) => setFormError(err.message),
  })

  const busy = save.isPending || test.isPending
  const secretSaved = !!saved?.has_secret
  const canSubmit = !busy && (f.kind === "path" ? f.path.trim() !== ""
    : f.kind === "sftp" ? f.host.trim() !== "" && f.user.trim() !== "" && f.host_key.trim() !== ""
    : f.bucket.trim() !== "" && f.access_key.trim() !== "")

  return (
    <form
      className="space-y-5 rounded-md border p-4"
      onSubmit={(e) => { e.preventDefault(); save.mutate() }}
    >
      <h3 className="font-semibold">{saved ? `Edit ${saved.name}` : "New destination"}</h3>

      {!saved && (
        <fieldset>
          <legend className={fieldClass}>Kind</legend>
          <div className="flex flex-wrap gap-4">
            {(Object.keys(KIND_WORDS) as Kind[]).map((k) => (
              <label key={k} className="flex items-center gap-2 text-sm">
                <input type="radio" name="bd-kind" checked={f.kind === k} onChange={() => { set("kind", k); setResults(null) }} />
                {KIND_WORDS[k]}
              </label>
            ))}
          </div>
        </fieldset>
      )}

      <div>
        <label htmlFor="bd-name" className={fieldClass}>Name</label>
        <Input id="bd-name" value={f.name} onChange={(e) => set("name", e.target.value)} placeholder="Optional" autoComplete="off" />
      </div>

      {f.kind === "s3" && (
        <>
          <div className="grid gap-5 md:grid-cols-3">
            <div className="md:col-span-2">
              <label htmlFor="bd-endpoint" className={fieldClass}>Endpoint</label>
              <Input id="bd-endpoint" value={f.endpoint} onChange={(e) => set("endpoint", e.target.value)} placeholder="s3.example.com"
                autoComplete="off" spellCheck={false} aria-describedby="bd-endpoint-help" />
              <p id="bd-endpoint-help" className="mt-2 text-xs text-muted-foreground">
                Host name of the storage service, with a port if it needs one. Leave empty for Amazon S3.
              </p>
            </div>
            <div>
              <label htmlFor="bd-region" className={fieldClass}>Region</label>
              <Input id="bd-region" value={f.region} onChange={(e) => set("region", e.target.value)} placeholder="us-east-1" autoComplete="off" spellCheck={false} />
            </div>
          </div>
          <div className="grid gap-5 md:grid-cols-2">
            <div>
              <label htmlFor="bd-bucket" className={fieldClass}>Bucket</label>
              <Input id="bd-bucket" value={f.bucket} onChange={(e) => set("bucket", e.target.value)} autoComplete="off" spellCheck={false} required />
            </div>
            <div>
              <label htmlFor="bd-prefix" className={fieldClass}>Path prefix</label>
              <Input id="bd-prefix" value={f.path_prefix} onChange={(e) => set("path_prefix", e.target.value)} placeholder="Optional" autoComplete="off" spellCheck={false} />
            </div>
          </div>
          <div className="grid gap-5 md:grid-cols-2">
            <div>
              <label htmlFor="bd-access-key" className={fieldClass}>Access key</label>
              <Input id="bd-access-key" value={f.access_key} onChange={(e) => set("access_key", e.target.value)} autoComplete="off" spellCheck={false} className="font-mono" required />
            </div>
            <div>
              <label htmlFor="bd-secret-key" className={fieldClass}>Secret key</label>
              <Input id="bd-secret-key" type="password" value={f.secret_key} onChange={(e) => set("secret_key", e.target.value)}
                placeholder={secretSaved ? "A key is saved. Leave empty to keep it." : "Enter the secret key"} autoComplete="new-password" />
            </div>
          </div>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" className="h-4 w-4 rounded border-input" checked={f.use_tls} onChange={(e) => set("use_tls", e.target.checked)} />
            Connect over HTTPS
            <span className="text-xs text-muted-foreground">Turn off only for a store on a private network that has no certificate.</span>
          </label>
        </>
      )}

      {f.kind === "sftp" && (
        <>
          <div className="grid gap-5 md:grid-cols-4">
            <div className="md:col-span-2">
              <label htmlFor="bd-host" className={fieldClass}>Host</label>
              <Input id="bd-host" value={f.host} onChange={(e) => set("host", e.target.value)} placeholder="backup.example.com" autoComplete="off" spellCheck={false} required />
            </div>
            <div>
              <label htmlFor="bd-port" className={fieldClass}>Port</label>
              <Input id="bd-port" inputMode="numeric" value={f.port} onChange={(e) => set("port", e.target.value)} autoComplete="off" />
            </div>
            <div>
              <label htmlFor="bd-user" className={fieldClass}>User</label>
              <Input id="bd-user" value={f.user} onChange={(e) => set("user", e.target.value)} autoComplete="off" spellCheck={false} required />
            </div>
          </div>
          <div>
            <label htmlFor="bd-hostkey" className={fieldClass}>Host key</label>
            <div className="flex flex-wrap gap-2">
              <Input id="bd-hostkey" value={f.host_key} onChange={(e) => { set("host_key", e.target.value); setFingerprint("") }}
                placeholder="ssh-ed25519 AAAA..." autoComplete="off" spellCheck={false} className="min-w-0 flex-1 basis-64 font-mono" aria-describedby="bd-hostkey-help" />
              <Button type="button" variant="outline" disabled={!f.host.trim() || scan.isPending} onClick={() => scan.mutate()}>
                {scan.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
                Read from server
              </Button>
            </div>
            <p id="bd-hostkey-help" className="mt-2 text-xs text-muted-foreground">
              A server presenting any other key is refused. Compare the fingerprint with the one your provider shows before saving.
              {fingerprint && <> Fingerprint: <span className="font-mono">{fingerprint}</span></>}
            </p>
          </div>
          <fieldset>
            <legend className={fieldClass}>Sign in with</legend>
            <div className="flex gap-4">
              <label className="flex items-center gap-2 text-sm">
                <input type="radio" name="bd-auth" checked={f.auth === "password"} onChange={() => set("auth", "password")} /> Password
              </label>
              <label className="flex items-center gap-2 text-sm">
                <input type="radio" name="bd-auth" checked={f.auth === "key"} onChange={() => set("auth", "key")} /> Private key
              </label>
            </div>
          </fieldset>
          {f.auth === "password" ? (
            <div>
              <label htmlFor="bd-password" className={fieldClass}>Password</label>
              <Input id="bd-password" type="password" value={f.password} onChange={(e) => set("password", e.target.value)}
                placeholder={secretSaved && saved?.auth === "password" ? "A password is saved. Leave empty to keep it." : "Enter the password"} autoComplete="new-password" />
            </div>
          ) : (
            <div>
              <label htmlFor="bd-key" className={fieldClass}>Private key</label>
              <textarea id="bd-key" value={f.private_key} onChange={(e) => set("private_key", e.target.value)} rows={4} spellCheck={false}
                placeholder={secretSaved && saved?.auth === "key" ? "A key is saved. Leave empty to keep it." : "-----BEGIN OPENSSH PRIVATE KEY-----"}
                className="w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-xs" aria-describedby="bd-key-help" />
              <p id="bd-key-help" className="mt-1 text-xs text-muted-foreground">A key without a passphrase. It is stored encrypted and never shown again.</p>
            </div>
          )}
          <div>
            <label htmlFor="bd-rpath" className={fieldClass}>Directory on the server</label>
            <Input id="bd-rpath" value={f.path} onChange={(e) => set("path", e.target.value)} placeholder="backups" autoComplete="off" spellCheck={false} />
          </div>
        </>
      )}

      {f.kind === "path" && (
        <div>
          <label htmlFor="bd-path" className={fieldClass}>Directory</label>
          <Input id="bd-path" value={f.path} onChange={(e) => set("path", e.target.value)} placeholder="/mnt/backups" autoComplete="off" spellCheck={false}
            className="font-mono" aria-describedby="bd-path-help" required />
          <p id="bd-path-help" className="mt-2 text-xs text-muted-foreground">
            Mount the file system (NFS, CIFS or a disk of its own) at this path on each node that backs up there. A node refuses a
            directory on its root file system, and refuses to write when the mount is gone. A disk mounted on one node holds that
            node&apos;s backups only: restore them on the same node.
          </p>
        </div>
      )}

      <fieldset className="rounded-md border p-4">
        <legend className="px-1 text-sm font-medium">Test from</legend>
        <p className="text-xs text-muted-foreground">
          Each one connects with the details above, writes a small file, reads it back and deletes it. Nothing is saved by a test.
        </p>
        <ul className="mt-3 space-y-2">
          {f.kind !== "path" && (
            <li className="flex items-center gap-2 text-sm">
              <input type="checkbox" className="h-4 w-4 rounded border-input" checked disabled aria-label="Panel" />
              Panel
            </li>
          )}
          {(nodes ?? []).map((n) => (
            <li key={n.id}>
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  className="h-4 w-4 rounded border-input"
                  checked={!skipped.has(n.id)}
                  onChange={(e) => {
                    const next = new Set(skipped)
                    if (e.target.checked) next.delete(n.id)
                    else next.add(n.id)
                    setSkipped(next)
                  }}
                />
                {n.name}
              </label>
            </li>
          ))}
        </ul>
        {results && (
          <ul role="status" className="mt-4 space-y-2 border-t pt-4">
            {results.map((r) => (
              <li key={r.node_id ?? "panel"} className="text-sm">
                <span className="inline-flex items-center gap-2 font-medium">
                  <span className={cn("h-2 w-2 rounded-full", RESULT_WORDS[r.status].lamp)} aria-hidden="true" />
                  {r.name}: {RESULT_WORDS[r.status].word}
                </span>
                {r.message && <p className="ml-4 mt-0.5 break-words text-xs text-muted-foreground">{r.message}</p>}
              </li>
            ))}
          </ul>
        )}
      </fieldset>

      {formError && (
        <p role="alert" className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300">
          {formError}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-3 border-t pt-5">
        <Button type="submit" disabled={!canSubmit}>
          {save.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
          Save
        </Button>
        <Button type="button" variant="outline" disabled={!canSubmit} onClick={() => test.mutate()}>
          {test.isPending && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
          {test.isPending ? "Testing" : "Test destination"}
        </Button>
        <Button type="button" variant="ghost" disabled={busy} onClick={onCancel}>Cancel</Button>
      </div>
    </form>
  )
}
