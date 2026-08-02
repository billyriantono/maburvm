"use client"

import { useState } from "react"
import Link from "next/link"
import { ArrowLeft, KeyRound, Trash2, Plus } from "lucide-react"
import { useSSHKeys, useCreateSSHKey, useDeleteSSHKey } from "@/lib/hooks/use-ssh-keys"

// Client SSH key management: customers add/remove the public keys that get
// injected into VMs they order (see the order page's SSH Keys step). Keys are
// scoped to the caller server-side, so this only ever shows/edits their own.
export default function ClientSSHKeysPage() {
  const { data: keys, isLoading } = useSSHKeys()
  const createKey = useCreateSSHKey()
  const deleteKey = useDeleteSSHKey()

  const [name, setName] = useState("")
  const [publicKey, setPublicKey] = useState("")
  const [error, setError] = useState("")

  const add = () => {
    setError("")
    if (!publicKey.trim()) {
      setError("Paste your SSH public key.")
      return
    }
    createKey.mutate(
      { name: name.trim() || undefined, public_key: publicKey.trim() },
      {
        onSuccess: () => { setName(""); setPublicKey("") },
        onError: (e: Error) => setError(e.message || "Failed to add key"),
      },
    )
  }

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex items-center gap-3">
        <Link href="/client/vms" className="p-2 rounded-md border bg-background hover:bg-muted transition-colors">
          <ArrowLeft className="w-5 h-5" />
        </Link>
        <KeyRound className="w-6 h-6 text-muted-foreground" />
        <h1 className="text-2xl font-semibold tracking-tight">SSH Keys</h1>
      </div>

      {/* Add key */}
      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b">
          <h2 className="text-lg font-semibold">Add a key</h2>
        </div>
        <div className="p-5 space-y-3">
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Label (optional, e.g. laptop)"
            className="w-full h-10 px-3 rounded-md border border-input bg-background text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          />
          <textarea
            value={publicKey}
            onChange={(e) => setPublicKey(e.target.value)}
            placeholder="ssh-ed25519 AAAA… user@host"
            rows={3}
            className="w-full px-3 py-2 rounded-md border border-input bg-background font-mono text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          />
          {error && <p className="text-sm text-destructive">{error}</p>}
          <button
            onClick={add}
            disabled={createKey.isPending}
            className="inline-flex items-center gap-2 h-10 px-4 rounded-md bg-primary text-primary-foreground text-sm font-medium shadow-sm hover:bg-primary/90 transition-colors disabled:opacity-50"
          >
            <Plus className="w-4 h-4" /> {createKey.isPending ? "Adding…" : "Add key"}
          </button>
        </div>
      </section>

      {/* Existing keys */}
      <section className="rounded-lg border bg-card text-card-foreground shadow-sm">
        <div className="px-5 py-4 border-b">
          <h2 className="text-lg font-semibold">Your keys</h2>
        </div>
        <div className="p-5">
          {isLoading ? (
            <p className="text-muted-foreground">Loading…</p>
          ) : !keys?.length ? (
            <p className="text-muted-foreground">No SSH keys yet. Add one above.</p>
          ) : (
            <div className="space-y-2">
              {keys.map((k) => (
                <div key={k.id} className="flex items-center justify-between gap-3 p-3 rounded-md border">
                  <div className="min-w-0">
                    <div className="font-medium">{k.name}</div>
                    <div className="text-xs font-mono text-muted-foreground truncate">{k.fingerprint}</div>
                  </div>
                  <button
                    onClick={() => deleteKey.mutate(k.id)}
                    disabled={deleteKey.isPending}
                    className="inline-flex items-center gap-1 h-9 px-3 rounded-md border border-input bg-background text-destructive text-xs font-medium hover:bg-destructive/10 transition-colors disabled:opacity-50"
                  >
                    <Trash2 className="w-4 h-4" /> Delete
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      </section>
    </div>
  )
}
