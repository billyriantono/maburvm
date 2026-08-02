"use client"

import { useState } from "react"
import { toast } from "sonner"
import {
  KeySquare,
  Plus,
  Copy,
  Trash2,
  AlertCircle,
  Loader2,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useSSHKeys, useCreateSSHKey, useDeleteSSHKey } from "@/lib/hooks/use-ssh-keys"
import type { SSHKey } from "@/types/ssh-key"

function formatDate(value?: string): string {
  if (!value) return "—"
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString()
}

export default function SSHKeysSettingsPage() {
  const { data: keys, isLoading, error } = useSSHKeys()
  const createKey = useCreateSSHKey()
  const deleteKey = useDeleteSSHKey()

  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState("")
  const [publicKey, setPublicKey] = useState("")
  const [deleteTarget, setDeleteTarget] = useState<SSHKey | null>(null)

  const handleCreate = async () => {
    const key = publicKey.trim()
    if (!key) {
      toast.error("Public key is required")
      return
    }
    try {
      await createKey.mutateAsync({ name: name.trim() || undefined, public_key: key })
      toast.success("SSH key added")
      setShowCreate(false)
      setName("")
      setPublicKey("")
    } catch (err) {
      toast.error("Failed to add SSH key", { description: (err as Error).message })
    }
  }

  const handleDelete = async () => {
    if (!deleteTarget) return
    try {
      await deleteKey.mutateAsync(deleteTarget.id)
      toast.success("SSH key deleted", { description: `"${deleteTarget.name}" removed.` })
      setDeleteTarget(null)
    } catch (err) {
      toast.error("Failed to delete SSH key", { description: (err as Error).message })
    }
  }

  const handleCopy = (value: string) => {
    navigator.clipboard.writeText(value)
    toast.success("Public key copied")
  }

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">SSH Keys</h1>
          <p className="text-muted-foreground text-sm mt-1">
            Public keys you can inject when creating or rebuilding a VM
          </p>
        </div>
        <Button onClick={() => setShowCreate(true)}>
          <Plus className="w-4 h-4 mr-2" />
          Add SSH Key
        </Button>
      </div>

      {/* List */}
      <Card>
        <CardHeader className="border-b">
          <CardTitle className="flex items-center gap-2">
            <KeySquare className="w-5 h-5" />
            Your SSH Keys
          </CardTitle>
          <CardDescription>Only public keys are stored. Select them at VM create or rebuild time.</CardDescription>
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
              <p className="font-medium">Failed to load SSH keys</p>
              <p className="text-sm text-muted-foreground mt-1">{(error as Error).message}</p>
            </div>
          ) : !keys || keys.length === 0 ? (
            <div className="p-12 text-center">
              <KeySquare className="w-12 h-12 text-muted-foreground/40 mx-auto mb-3" />
              <p className="font-medium">No SSH keys yet</p>
              <p className="text-sm text-muted-foreground mt-1 mb-4">
                Add a public key to use passwordless login on new VMs.
              </p>
              <Button onClick={() => setShowCreate(true)}>
                <Plus className="w-4 h-4 mr-2" />
                Add SSH Key
              </Button>
            </div>
          ) : (
            <ul className="divide-y">
              {keys.map((key) => (
                <li key={key.id} className="p-4 flex items-center justify-between gap-4">
                  <div className="min-w-0">
                    <span className="font-medium text-foreground truncate block">{key.name}</span>
                    <code className="text-xs font-mono text-muted-foreground break-all">{key.fingerprint}</code>
                    <p className="text-xs text-muted-foreground mt-1">Added {formatDate(key.created_at)}</p>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <Button variant="outline" size="sm" onClick={() => handleCopy(key.public_key)}>
                      <Copy className="w-4 h-4" />
                    </Button>
                    <Button variant="destructive" size="sm" onClick={() => setDeleteTarget(key)}>
                      <Trash2 className="w-4 h-4 mr-2" />
                      Delete
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      {/* Create dialog */}
      <Dialog open={showCreate} onOpenChange={(open) => !open && setShowCreate(false)}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Plus className="w-5 h-5" />
              Add SSH Key
            </DialogTitle>
            <DialogDescription>Paste a public key (e.g. the contents of id_ed25519.pub).</DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div>
              <label htmlFor="ssh-name" className="block text-sm font-medium text-muted-foreground mb-2">
                Name (optional)
              </label>
              <Input
                id="ssh-name"
                placeholder="e.g. laptop"
                value={name}
                onChange={(e) => setName(e.target.value)}
                maxLength={100}
              />
              <p className="text-xs text-muted-foreground mt-1">Defaults to the key comment or fingerprint if left blank.</p>
            </div>
            <div>
              <label htmlFor="ssh-key" className="block text-sm font-medium text-muted-foreground mb-2">
                Public Key
              </label>
              <textarea
                id="ssh-key"
                placeholder="ssh-ed25519 AAAA... user@host"
                value={publicKey}
                onChange={(e) => setPublicKey(e.target.value)}
                rows={4}
                className="w-full rounded-md border border-input bg-background p-3 font-mono text-xs resize-y focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setShowCreate(false)}>
              Cancel
            </Button>
            <Button onClick={handleCreate} disabled={createKey.isPending}>
              {createKey.isPending ? (
                <><Loader2 className="w-4 h-4 mr-2 animate-spin" />Adding...</>
              ) : (
                <><Plus className="w-4 h-4 mr-2" />Add Key</>
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirmation */}
      <Dialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Trash2 className="w-5 h-5" />
              Delete SSH Key
            </DialogTitle>
            <DialogDescription>
              Remove &quot;{deleteTarget?.name}&quot;? This won&apos;t change keys already injected into existing VMs.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteTarget(null)}>Cancel</Button>
            <Button variant="destructive" onClick={handleDelete} disabled={deleteKey.isPending}>
              {deleteKey.isPending ? <Loader2 className="w-4 h-4 mr-2 animate-spin" /> : <Trash2 className="w-4 h-4 mr-2" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
