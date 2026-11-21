"use client"

import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Loader2, PlugZap, RefreshCw, Save, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import { useNodes } from "@/lib/hooks/use-nodes"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useConfirm } from "@/components/confirm-provider"
import { useFlash } from "@/lib/use-feedback"

interface BackupDestinationStatus {
  configured: boolean
  endpoint: string
  region: string
  bucket: string
  path_prefix: string
  access_key: string
  has_secret_key: boolean
  use_tls: boolean
}

interface TestResult {
  node_id?: string
  name: string
  status: "ok" | "failed" | "needs_agent_update" | "unreachable"
  message?: string
}

const QUERY_KEY = ["settings", "backup-destination"]
const ENDPOINT = "/api/v1/settings/system/backup-destination"

const RESULT_WORDS: Record<TestResult["status"], { word: string; lamp: string }> = {
  ok: { word: "Works", lamp: "bg-success" },
  failed: { word: "Failed", lamp: "bg-destructive" },
  needs_agent_update: { word: "Needs an agent update", lamp: "bg-warning" },
  unreachable: { word: "Node not reached", lamp: "bg-destructive" },
}

// Where backups are kept: one S3-compatible bucket, used by the panel and sent
// to each node with every backup and restore.
//
// The secret key is write-only. The server stores it encrypted and only ever
// says whether one is saved: the field is empty on load, and leaving it empty
// keeps the saved key.
export function BackupDestinationSettings() {
  const queryClient = useQueryClient()
  const confirm = useConfirm()
  const saved = useFlash()
  const { data: status, isLoading, isError, error, refetch } = useQuery<BackupDestinationStatus>({
    queryKey: QUERY_KEY,
    queryFn: async () => (await api.get<BackupDestinationStatus>(ENDPOINT)).data.data,
  })
  const { data: nodes } = useNodes()

  const [endpoint, setEndpoint] = useState("")
  const [region, setRegion] = useState("")
  const [bucket, setBucket] = useState("")
  const [pathPrefix, setPathPrefix] = useState("")
  const [accessKey, setAccessKey] = useState("")
  const [secretKey, setSecretKey] = useState("")
  const [useTls, setUseTls] = useState(true)
  const [formError, setFormError] = useState<string | null>(null)
  // Nodes left out of the test. Kept as exclusions so a node added later is tested by default.
  const [skipped, setSkipped] = useState<Set<string>>(new Set())
  const [results, setResults] = useState<TestResult[] | null>(null)

  useEffect(() => {
    if (!status) return
    setEndpoint(status.endpoint)
    setRegion(status.region)
    setBucket(status.bucket)
    setPathPrefix(status.path_prefix)
    setAccessKey(status.access_key)
    setUseTls(status.use_tls)
  }, [status])

  const body = () => ({
    endpoint: endpoint.trim(),
    region: region.trim(),
    bucket: bucket.trim(),
    path_prefix: pathPrefix.trim(),
    access_key: accessKey.trim(),
    secret_key: secretKey,
    use_tls: useTls,
  })

  const save = useMutation<BackupDestinationStatus, Error, void>({
    mutationFn: async () => (await api.put<BackupDestinationStatus>(ENDPOINT, body())).data.data,
    onSuccess: (next) => {
      queryClient.setQueryData(QUERY_KEY, next)
      setSecretKey("")
      setFormError(null)
      saved.trigger()
      toast.success("Backup destination saved")
    },
    onError: (err) => setFormError(err.message),
  })

  const test = useMutation<TestResult[], Error, void>({
    mutationFn: async () => {
      const node_ids = (nodes ?? []).filter((n) => !skipped.has(n.id)).map((n) => n.id)
      const res = await api.post<{ results: TestResult[] }>(`${ENDPOINT}/test`, { ...body(), node_ids })
      return res.data.data.results
    },
    onMutate: () => setResults(null),
    onSuccess: (next) => {
      setFormError(null)
      setResults(next)
    },
    onError: (err) => setFormError(err.message),
  })

  const remove = useMutation<BackupDestinationStatus, Error, void>({
    mutationFn: async () => (await api.delete<BackupDestinationStatus>(ENDPOINT)).data.data,
    onSuccess: (next) => {
      queryClient.setQueryData(QUERY_KEY, next)
      setSecretKey("")
      setResults(null)
      toast.success("Backup destination removed")
    },
  })

  const keySaved = !!status?.configured && status.has_secret_key
  const busy = save.isPending || test.isPending || remove.isPending
  const canSubmit = bucket.trim() !== "" && accessKey.trim() !== "" && !busy

  return (
    <Card>
      <CardHeader className="border-b">
        <CardTitle className="text-xl">Backup destination</CardTitle>
        <CardDescription>
          The S3-compatible bucket backups are written to and restored from. Every node must be able to reach it, and so
          must the panel, which deletes backups from it.
        </CardDescription>
      </CardHeader>
      <CardContent className="p-6">
        {isLoading ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Loading the backup destination
          </p>
        ) : isError ? (
          <div>
            <p className="text-sm font-medium text-destructive">Could not load the backup destination</p>
            <p className="mt-1 text-sm text-muted-foreground">{(error as Error)?.message}</p>
            <Button type="button" variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>
              <RefreshCw className="h-4 w-4" aria-hidden="true" />
              Retry
            </Button>
          </div>
        ) : (
          <form
            className="space-y-5"
            onSubmit={(e) => {
              e.preventDefault()
              save.mutate()
            }}
          >
            <p role="status" className="rounded-md border bg-muted px-3 py-2 text-sm">
              {status?.configured
                ? `In use: bucket ${status.bucket}${status.endpoint ? ` at ${status.endpoint}` : ""}.`
                : "Not set. A node can only back up with storage keys in its own environment file, and the panel cannot delete or move those backups."}
            </p>

            <div className="grid gap-5 md:grid-cols-3">
              <div className="md:col-span-2">
                <label htmlFor="bd-endpoint" className="mb-2 block text-sm font-medium">
                  Endpoint
                </label>
                <Input
                  id="bd-endpoint"
                  value={endpoint}
                  onChange={(e) => setEndpoint(e.target.value)}
                  placeholder="s3.example.com"
                  autoComplete="off"
                  spellCheck={false}
                  aria-describedby="bd-endpoint-help"
                />
                <p id="bd-endpoint-help" className="mt-2 text-xs text-muted-foreground">
                  Host name of the storage service, with a port if it needs one. Leave empty for Amazon S3.
                </p>
              </div>
              <div>
                <label htmlFor="bd-region" className="mb-2 block text-sm font-medium">
                  Region
                </label>
                <Input
                  id="bd-region"
                  value={region}
                  onChange={(e) => setRegion(e.target.value)}
                  placeholder="us-east-1"
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
            </div>

            <div className="grid gap-5 md:grid-cols-2">
              <div>
                <label htmlFor="bd-bucket" className="mb-2 block text-sm font-medium">
                  Bucket
                </label>
                <Input
                  id="bd-bucket"
                  value={bucket}
                  onChange={(e) => setBucket(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  required
                />
              </div>
              <div>
                <label htmlFor="bd-prefix" className="mb-2 block text-sm font-medium">
                  Path prefix
                </label>
                <Input
                  id="bd-prefix"
                  value={pathPrefix}
                  onChange={(e) => setPathPrefix(e.target.value)}
                  placeholder="Optional"
                  autoComplete="off"
                  spellCheck={false}
                  aria-describedby="bd-prefix-help"
                />
                <p id="bd-prefix-help" className="mt-2 text-xs text-muted-foreground">
                  A folder inside the bucket for new backups. Backups already made stay where they are.
                </p>
              </div>
            </div>

            <div className="grid gap-5 md:grid-cols-2">
              <div>
                <label htmlFor="bd-access-key" className="mb-2 block text-sm font-medium">
                  Access key
                </label>
                <Input
                  id="bd-access-key"
                  value={accessKey}
                  onChange={(e) => setAccessKey(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono"
                  required
                />
              </div>
              <div>
                <label htmlFor="bd-secret-key" className="mb-2 block text-sm font-medium">
                  Secret key
                </label>
                <Input
                  id="bd-secret-key"
                  type="password"
                  value={secretKey}
                  onChange={(e) => setSecretKey(e.target.value)}
                  placeholder={keySaved ? "A key is saved. Leave empty to keep it." : "Enter the secret key"}
                  autoComplete="new-password"
                  aria-describedby="bd-secret-key-help"
                />
              </div>
            </div>
            <p id="bd-secret-key-help" className="text-xs text-muted-foreground">
              The secret key is stored encrypted and never shown again. It is sent to a node only with a backup or a
              restore. If you change the endpoint or the bucket you must enter it again.
            </p>

            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                className="h-4 w-4 rounded border-input"
                checked={useTls}
                onChange={(e) => setUseTls(e.target.checked)}
              />
              Connect over HTTPS
              <span className="text-xs text-muted-foreground">Turn off only for a store on a private network that has no certificate.</span>
            </label>

            <fieldset className="rounded-md border p-4">
              <legend className="px-1 text-sm font-medium">Test from</legend>
              <p className="text-xs text-muted-foreground">
                Each one connects with the details above, writes a small file, reads it back and deletes it. Nothing is
                saved by a test.
              </p>
              <ul className="mt-3 space-y-2">
                <li className="flex items-center gap-2 text-sm">
                  <input type="checkbox" className="h-4 w-4 rounded border-input" checked disabled aria-label="Panel" />
                  Panel
                </li>
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
                {save.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                ) : saved.active() ? (
                  <Check className="h-4 w-4" aria-hidden="true" />
                ) : (
                  <Save className="h-4 w-4" aria-hidden="true" />
                )}
                {saved.active() ? "Saved" : "Save"}
              </Button>
              <Button type="button" variant="outline" disabled={!canSubmit} onClick={() => test.mutate()}>
                {test.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                ) : (
                  <PlugZap className="h-4 w-4" aria-hidden="true" />
                )}
                {test.isPending ? "Testing" : "Test destination"}
              </Button>
              {status?.configured && (
                <Button
                  type="button"
                  variant="ghost"
                  className="ml-auto text-destructive hover:text-destructive"
                  disabled={busy}
                  onClick={async () => {
                    await confirm({
                      title: "Remove the backup destination?",
                      description:
                        "The saved bucket and keys are deleted from the panel. Backups already in the bucket stay there, but they can no longer be restored or deleted from the panel until the destination is saved again. New backups work only on nodes that have storage keys of their own.",
                      confirmLabel: "Remove",
                      destructive: true,
                      action: () => remove.mutateAsync(),
                    })
                  }}
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                  Remove
                </Button>
              )}
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  )
}
