"use client"

import { useState } from "react"
import { toast } from "sonner"
import {
  KeyRound,
  Plus,
  Copy,
  Trash2,
  AlertTriangle,
  AlertCircle,
  Loader2,
  Terminal,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useAPIKeys, useCreateAPIKey, useRevokeAPIKey } from "@/lib/hooks/use-api-keys"
import type { APIKey } from "@/types/api-key"

function formatDate(value?: string): string {
  if (!value) return "—"
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString()
}

function isExpired(key: APIKey): boolean {
  return !!key.expires_at && new Date(key.expires_at).getTime() < Date.now()
}

export default function APIKeysSettingsPage() {
  const { data: keys, isLoading, error } = useAPIKeys()
  const createKey = useCreateAPIKey()
  const revokeKey = useRevokeAPIKey()

  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState("")
  const [expiresAt, setExpiresAt] = useState("")
  const [newToken, setNewToken] = useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<APIKey | null>(null)

  const handleCreate = async () => {
    const trimmed = name.trim()
    if (!trimmed) {
      toast.error("Name is required")
      return
    }
    try {
      const created = await createKey.mutateAsync({
        name: trimmed,
        // Datetime-local has no timezone; treat as local and send ISO.
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : undefined,
      })
      setNewToken(created.token)
      setShowCreate(false)
      setName("")
      setExpiresAt("")
    } catch (err) {
      toast.error("Failed to create API key", { description: (err as Error).message })
    }
  }

  const handleRevoke = async () => {
    if (!revokeTarget) return
    try {
      await revokeKey.mutateAsync(revokeTarget.id)
      toast.success("API key revoked", { description: `"${revokeTarget.name}" can no longer be used.` })
      setRevokeTarget(null)
    } catch (err) {
      toast.error("Failed to revoke API key", { description: (err as Error).message })
    }
  }

  const handleCopyToken = () => {
    if (!newToken) return
    navigator.clipboard.writeText(newToken)
    toast.success("API key copied to clipboard")
  }

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">API Keys</h1>
          <p className="text-muted-foreground text-sm mt-1">
            Long-lived credentials for automation and the public API
          </p>
        </div>
        <Button onClick={() => setShowCreate(true)}>
          <Plus className="w-4 h-4 mr-2" />
          Create API Key
        </Button>
      </div>

      {/* Usage hint */}
      <Card className="mb-6">
        <CardContent className="p-4 flex items-start gap-3">
          <Terminal className="w-5 h-5 mt-0.5 shrink-0 text-muted-foreground" />
          <div className="text-sm">
            <p className="font-medium text-foreground">Authenticate requests with your key</p>
            <p className="text-muted-foreground mt-1">
              Pass it as a header:{" "}
              <code className="text-xs bg-muted px-1.5 py-0.5 rounded-md border">
                X-API-Key: mvk_…
              </code>{" "}
              or{" "}
              <code className="text-xs bg-muted px-1.5 py-0.5 rounded-md border">
                Authorization: Bearer mvk_…
              </code>
            </p>
          </div>
        </CardContent>
      </Card>

      {/* List */}
      <Card>
        <CardHeader className="border-b">
          <CardTitle className="flex items-center gap-2">
            <KeyRound className="w-5 h-5" />
            Your API Keys
          </CardTitle>
          <CardDescription>Keys are shown by prefix only; the full token is revealed once at creation</CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {isLoading ? (
            <div className="p-6 space-y-3">
              <Skeleton className="h-16" />
              <Skeleton className="h-16" />
            </div>
          ) : error ? (
            <div className="p-12 text-center">
              <AlertCircle className="w-12 h-12 text-destructive mx-auto mb-3" />
              <p className="font-medium">Failed to load API keys</p>
              <p className="text-sm text-muted-foreground mt-1">{(error as Error).message}</p>
            </div>
          ) : !keys || keys.length === 0 ? (
            <div className="p-12 text-center">
              <KeyRound className="w-12 h-12 text-muted-foreground/40 mx-auto mb-3" />
              <p className="font-medium">No API keys yet</p>
              <p className="text-sm text-muted-foreground mt-1 mb-4">
                Create a key to access the MaburVM API from scripts and CI.
              </p>
              <Button onClick={() => setShowCreate(true)}>
                <Plus className="w-4 h-4 mr-2" />
                Create API Key
              </Button>
            </div>
          ) : (
            <ul className="divide-y">
              {keys.map((key) => {
                const expired = isExpired(key)
                return (
                  <li key={key.id} className="p-4 flex items-center justify-between gap-4">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="font-medium text-foreground truncate">{key.name}</span>
                        {expired ? (
                          <Badge variant="destructive" className="text-[10px]">Expired</Badge>
                        ) : key.is_active ? (
                          <Badge variant="success" className="text-[10px]">Active</Badge>
                        ) : (
                          <Badge variant="secondary" className="text-[10px]">Revoked</Badge>
                        )}
                      </div>
                      <code className="text-xs font-mono text-muted-foreground">{key.prefix}••••••••</code>
                      <p className="text-xs text-muted-foreground mt-1">
                        Created {formatDate(key.created_at)} · Last used {formatDate(key.last_used_at)}
                        {key.expires_at ? ` · Expires ${formatDate(key.expires_at)}` : ""}
                      </p>
                    </div>
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={() => setRevokeTarget(key)}
                    >
                      <Trash2 className="w-4 h-4 mr-2" />
                      Revoke
                    </Button>
                  </li>
                )
              })}
            </ul>
          )}
        </CardContent>
      </Card>

      {/* Create dialog */}
      <Dialog open={showCreate} onOpenChange={(open) => !open && setShowCreate(false)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Plus className="w-5 h-5" />
              Create API Key
            </DialogTitle>
            <DialogDescription>Give your key a recognizable name. Expiry is optional.</DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div>
              <label htmlFor="key-name" className="block text-sm font-medium text-muted-foreground mb-2">
                Name
              </label>
              <Input
                id="key-name"
                placeholder="e.g. ci-deploy-bot"
                value={name}
                onChange={(e) => setName(e.target.value)}
                maxLength={100}
              />
            </div>
            <div>
              <label htmlFor="key-expiry" className="block text-sm font-medium text-muted-foreground mb-2">
                Expires (optional)
              </label>
              <Input
                id="key-expiry"
                type="datetime-local"
                value={expiresAt}
                onChange={(e) => setExpiresAt(e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setShowCreate(false)}>
              Cancel
            </Button>
            <Button onClick={handleCreate} disabled={createKey.isPending}>
              {createKey.isPending ? (
                <>
                  <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                  Creating...
                </>
              ) : (
                <>
                  <Plus className="w-4 h-4 mr-2" />
                  Create Key
                </>
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* One-time token reveal dialog */}
      <Dialog open={!!newToken} onOpenChange={(open) => !open && setNewToken(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <KeyRound className="w-5 h-5" />
              Save your API key
            </DialogTitle>
            <DialogDescription>This is the only time the full key is shown.</DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <code className="block text-sm font-mono bg-muted px-3 py-2 rounded-md border break-all">
              {newToken}
            </code>
            <div className="flex items-center gap-2 p-3 rounded-md border border-destructive/30 bg-destructive/10 text-destructive">
              <AlertTriangle className="w-4 h-4 shrink-0" />
              <p className="text-xs font-medium">
                Copy it now &mdash; you won&apos;t be able to see it again.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="secondary" onClick={handleCopyToken}>
              <Copy className="w-4 h-4 mr-2" />
              Copy Key
            </Button>
            <Button onClick={() => setNewToken(null)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Revoke confirm dialog */}
      <Dialog open={!!revokeTarget} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle className="w-5 h-5 text-destructive" />
              Revoke API key?
            </DialogTitle>
            <DialogDescription>
              Any automation using <span className="font-semibold">{revokeTarget?.name}</span> will immediately stop working. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRevokeTarget(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleRevoke} disabled={revokeKey.isPending}>
              {revokeKey.isPending ? (
                <>
                  <Loader2 className="w-4 h-4 mr-2 animate-spin" />
                  Revoking...
                </>
              ) : (
                <>
                  <Trash2 className="w-4 h-4 mr-2" />
                  Revoke Key
                </>
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
