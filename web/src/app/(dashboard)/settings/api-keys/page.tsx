"use client"

import { useBranding } from "@/lib/branding/provider"
import { useState } from "react"
import { toast } from "sonner"
import {
  KeyRound,
  Plus,
  Copy,
  Check,
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
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"
import { useCurrentUser } from "@/lib/hooks/use-auth"
import { useCopy } from "@/lib/use-feedback"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useAPIKeys, useCreateAPIKey, useRevokeAPIKey } from "@/lib/hooks/use-api-keys"
import type { APIKey, APIKeyScope } from "@/types/api-key"

// What a key may do, narrowest last. Every scope stays within what its owner
// may do; billing is for staff keys a billing system uses.
const SCOPES: { value: APIKeyScope; label: string; badge: string; help: string; staffOnly?: boolean }[] = [
  { value: "full", label: "Full access", badge: "", help: "Everything you can do yourself." },
  { value: "read_only", label: "Read only", badge: "Read-only", help: "Can view and monitor, but not change anything. Good for AI agents that only watch." },
  { value: "vm_power", label: "Power only", badge: "Power only", help: "Can list servers and start, shut down or restart them. Nothing else." },
  { value: "billing", label: "Billing", badge: "Billing only", help: "Billing, plans, customer accounts and usage, for a billing system. Never staff accounts or roles. Within what your staff role allows.", staffOnly: true },
]

function scopeOf(key: APIKey): APIKeyScope {
  return key.scope || (key.read_only ? "read_only" : "full")
}

function formatDate(value?: string): string {
  if (!value) return "—"
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString()
}

function isExpired(key: APIKey): boolean {
  return !!key.expires_at && new Date(key.expires_at).getTime() < Date.now()
}

export default function APIKeysSettingsPage() {
  const { branding } = useBranding()
  const { data, isLoading, error, refetch } = useAPIKeys()
  const keys = data?.keys
  const createKey = useCreateAPIKey()
  const revokeKey = useRevokeAPIKey()
  const { data: currentUser } = useCurrentUser()
  const isAdmin = currentUser?.role === "admin"

  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState("")
  const [expiresAt, setExpiresAt] = useState("")
  const [scope, setScope] = useState<APIKeyScope>("full")
  const [allowedIPs, setAllowedIPs] = useState("")
  const [asClient, setAsClient] = useState(false)
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
        scope,
        // The panel checks each entry and says which one it refused.
        allowed_ips: allowedIPs.split(/[\s,]+/).filter(Boolean),
        role: asClient ? "client" : undefined,
      })
      setNewToken(created.token)
      setShowCreate(false)
      setName("")
      setExpiresAt("")
      setScope("full")
      setAllowedIPs("")
      setAsClient(false)
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

  const { copy, copied } = useCopy()
  const handleCopyToken = () => {
    if (!newToken) return
    copy(newToken, "api-key")
  }

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">API Keys</h1>
          <p className="text-muted-foreground text-sm mt-1">
            {currentUser && !isAdmin
              ? "For your own scripts and tools. A key acts as you and reaches only your own servers."
              : "Long-lived credentials for automation and the public API"}
          </p>
        </div>
        <Button onClick={() => setShowCreate(true)}>
          <Plus className="w-4 h-4 mr-2" />
          Create API Key
        </Button>
      </div>

      {data && !data.apiEnabled && (
        <div role="alert" className="mb-6 rounded-md border border-warning/50 bg-warning/10 p-4 text-sm">
          <p className="font-semibold">API access is turned off on this panel</p>
          <p className="mt-1">
            Every key is refused until it is turned on again.{" "}
            {isAdmin ? "Turn it on under Settings, System, API." : "Ask your provider if you need it."}
          </p>
        </div>
      )}

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
            <p className="text-muted-foreground mt-1">
              Every endpoint, its fields and who may call it:{" "}
              <a href="/docs/api" className="font-medium text-foreground underline underline-offset-4">API reference</a>
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
              <p className="text-sm text-muted-foreground mt-1 mb-4">{(error as Error).message}</p>
              <Button variant="outline" onClick={() => refetch()}>Retry</Button>
            </div>
          ) : !keys || keys.length === 0 ? (
            <div className="p-12 text-center">
              <KeyRound className="w-12 h-12 text-muted-foreground/40 mx-auto mb-3" />
              <p className="font-medium">No API keys yet</p>
              <p className="text-sm text-muted-foreground mt-1 mb-4">
                Create a key to access the {branding.name} API from scripts and CI.
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
                const scopeBadge = SCOPES.find((s) => s.value === scopeOf(key))?.badge
                const ips = key.allowed_ips ?? []
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
                        {scopeBadge && <Badge variant="outline" className="text-[10px]">{scopeBadge}</Badge>}
                        {key.role === "client" && <Badge variant="outline" className="text-[10px]">Own resources only</Badge>}
                      </div>
                      <code className="text-xs font-mono text-muted-foreground">{key.prefix}••••••••</code>
                      <p className="text-xs text-muted-foreground mt-1">
                        Created {formatDate(key.created_at)} · Last used {formatDate(key.last_used_at)}
                        {key.expires_at ? ` · Expires ${formatDate(key.expires_at)}` : ""}
                      </p>
                      <p className="text-xs text-muted-foreground mt-1">
                        {ips.length === 0 ? "Works from any address" : <>Works only from <span className="font-mono">{ips.join(", ")}</span></>}
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
            <DialogDescription>
              Name the key and choose what it may do. Limiting where it works from and when it stops is optional.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 max-h-[65vh] overflow-y-auto pr-1">
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
            <fieldset>
              <legend className="block text-sm font-medium text-muted-foreground mb-2">What the key may do</legend>
              <div className="space-y-2">
                {SCOPES.filter((s) => isAdmin || !s.staffOnly).map((s) => (
                  <label
                    key={s.value}
                    className={cn(
                      "flex items-start gap-3 rounded-md border p-3 cursor-pointer",
                      scope === s.value && "border-primary"
                    )}
                  >
                    <input
                      type="radio"
                      name="key-scope"
                      value={s.value}
                      checked={scope === s.value}
                      onChange={() => setScope(s.value)}
                      className="mt-0.5 h-4 w-4 accent-primary"
                    />
                    <span className="grid gap-0.5">
                      <span className="text-sm font-medium">{s.label}</span>
                      <span className="text-xs text-muted-foreground">{s.help}</span>
                    </span>
                  </label>
                ))}
              </div>
            </fieldset>
            <div>
              <label htmlFor="key-ips" className="block text-sm font-medium text-muted-foreground mb-2">
                Works only from (optional)
              </label>
              <textarea
                id="key-ips"
                aria-describedby="key-ips-help"
                placeholder={"203.0.113.10\n198.51.100.0/24"}
                value={allowedIPs}
                onChange={(e) => setAllowedIPs(e.target.value)}
                rows={3}
                className="w-full rounded-md border border-input bg-background p-3 font-mono text-xs resize-y focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              />
              <p id="key-ips-help" className="text-xs text-muted-foreground mt-1">
                Addresses or ranges, one per line. Empty means anywhere. Requests from anywhere else are refused, and a
                key limited this way cannot make keys or change account settings.
              </p>
            </div>
            {isAdmin && (
              <div className="flex items-start gap-2">
                <Checkbox id="key-as-client" checked={asClient} onCheckedChange={(v) => setAsClient(v === true)} />
                <div className="grid gap-1">
                  <Label htmlFor="key-as-client">Own resources only</Label>
                  <p className="text-xs text-muted-foreground">The key acts as a regular user: only your own VMs, no admin pages.</p>
                </div>
              </div>
            )}
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
              {copied("api-key") ? <Check className="w-4 h-4 mr-2" aria-hidden="true" /> : <Copy className="w-4 h-4 mr-2" aria-hidden="true" />}
              {copied("api-key") ? "Copied" : "Copy Key"}
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
