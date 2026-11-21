"use client"

import { useState } from "react"
import { Loader2, Plus } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useConfirm } from "@/components/confirm-provider"
import { useCan, useCurrentUser } from "@/lib/hooks/use-auth"
import { useCreateRecipe, useDeleteRecipe, useRecipes, useUpdateRecipe } from "@/lib/hooks/use-recipes"
import type { Recipe, RecipeInput } from "@/types/recipe"

const SAMPLE = "#!/bin/bash\napt-get update && apt-get install -y nginx\necho 'server_name {{domain}};' > /etc/nginx/conf.d/site.conf"

interface Draft {
  id?: string
  name: string
  description: string
  script: string
  inputs: RecipeInput[]
  is_public: boolean
}

const empty: Draft = { name: "", description: "", script: "", inputs: [], is_public: false }

function InputsEditor({ inputs, onChange }: { inputs: RecipeInput[]; onChange: (v: RecipeInput[]) => void }) {
  const set = (i: number, patch: Partial<RecipeInput>) => onChange(inputs.map((x, n) => (n === i ? { ...x, ...patch } : x)))
  return (
    <fieldset className="space-y-2">
      <legend className="text-sm font-medium">Inputs</legend>
      <p className="text-xs text-muted-foreground">
        Values asked for when ordering. Write each one in the script as <code className="font-mono">{"{{key}}"}</code>. A value is one line of text.
      </p>
      {inputs.map((input, i) => (
        <div key={i} className="flex flex-wrap items-end gap-2 rounded-md border p-2">
          <div>
            <Label htmlFor={`in-key-${i}`} className="text-xs">Key</Label>
            <Input id={`in-key-${i}`} value={input.key} onChange={(e) => set(i, { key: e.target.value })} placeholder="domain" className="w-32 font-mono" />
          </div>
          <div>
            <Label htmlFor={`in-label-${i}`} className="text-xs">Label</Label>
            <Input id={`in-label-${i}`} value={input.label} onChange={(e) => set(i, { label: e.target.value })} placeholder="Domain name" className="w-40" />
          </div>
          <div>
            <Label htmlFor={`in-def-${i}`} className="text-xs">Default</Label>
            <Input id={`in-def-${i}`} value={input.default ?? ""} onChange={(e) => set(i, { default: e.target.value })} className="w-32" />
          </div>
          <label className="flex h-10 items-center gap-2 text-sm">
            <input type="checkbox" checked={input.required} onChange={(e) => set(i, { required: e.target.checked })} />Required
          </label>
          <label className="flex h-10 items-center gap-2 text-sm">
            <input type="checkbox" checked={!!input.secret} onChange={(e) => set(i, { secret: e.target.checked })} />Secret
          </label>
          <Button size="sm" variant="ghost" onClick={() => onChange(inputs.filter((_, n) => n !== i))}>Remove</Button>
        </div>
      ))}
      <Button size="sm" variant="outline" onClick={() => onChange([...inputs, { key: "", label: "", required: false }])}>Add an input</Button>
    </fieldset>
  )
}

function RecipeRow({ recipe, canEdit, onEdit, onDelete }: { recipe: Recipe; canEdit: boolean; onEdit: () => void; onDelete: () => void }) {
  const [open, setOpen] = useState(false)
  const inputs = recipe.inputs ?? []
  return (
    <div className="space-y-2 px-5 py-4">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
        <div className="min-w-0">
          <p className="font-medium">{recipe.name}</p>
          {recipe.description && <p className="text-sm text-muted-foreground">{recipe.description}</p>}
        </div>
        {inputs.length > 0 && (
          <span className="text-sm text-muted-foreground">Asks for {inputs.map((i) => i.label || i.key).join(", ")}</span>
        )}
        <span className="ml-auto flex gap-2">
          <Button size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen(!open)}>{open ? "Hide script" : "Show script"}</Button>
          {canEdit && <Button size="sm" variant="outline" onClick={onEdit}>Edit</Button>}
          {canEdit && <Button size="sm" variant="outline" onClick={onDelete}>Delete</Button>}
        </span>
      </div>
      {open && <pre className="max-h-72 overflow-auto rounded-md border bg-muted p-3 font-mono text-xs">{recipe.script}</pre>}
    </div>
  )
}

// Recipes: first-boot scripts. Everyone keeps their own; staff who manage
// templates also publish some to every customer, which makes the app catalog
// on the order page.
export function RecipeManager({ mode }: { mode: "admin" | "client" }) {
  const confirm = useConfirm()
  const can = useCan()
  const { data: me } = useCurrentUser()
  const { data: recipes, isLoading } = useRecipes()
  const create = useCreateRecipe()
  const update = useUpdateRecipe()
  const remove = useDeleteRecipe()
  const [draft, setDraft] = useState<Draft | null>(null)
  const publisher = mode === "admin" && can("template:update")

  const own = (recipes ?? []).filter((r) => !r.is_public && r.user_id === me?.id)
  const catalog = (recipes ?? []).filter((r) => r.is_public)
  const set = (patch: Partial<Draft>) => setDraft((d) => (d ? { ...d, ...patch } : d))
  const edit = (r: Recipe) => setDraft({ id: r.id, name: r.name, description: r.description, script: r.script, inputs: r.inputs ?? [], is_public: r.is_public })
  const del = (r: Recipe) => confirm({
    title: `Delete the recipe "${r.name}"?`,
    description: r.is_public ? "Customers no longer see it on the order page. Servers already built with it are not touched." : "Servers already built with it are not touched.",
    confirmLabel: "Delete recipe",
    destructive: true,
    action: async () => { await remove.mutateAsync(r.id); toast.success("Recipe deleted") },
  })
  const save = async () => {
    if (!draft) return
    const data = { name: draft.name.trim(), description: draft.description.trim(), script: draft.script, inputs: draft.inputs, is_public: publisher && draft.is_public }
    try {
      if (draft.id) await update.mutateAsync({ id: draft.id, data })
      else await create.mutateAsync(data)
      toast.success(draft.id ? "Recipe saved" : "Recipe added")
      setDraft(null)
    } catch (e) {
      toast.error((e as Error).message)
    }
  }

  const list = (rows: Recipe[], canEdit: boolean, emptyText: string) =>
    isLoading ? <p className="p-5 text-sm text-muted-foreground">Loading…</p>
      : rows.length === 0 ? <p className="p-5 text-sm text-muted-foreground">{emptyText}</p>
      : <div className="divide-y">{rows.map((r) => <RecipeRow key={r.id} recipe={r} canEdit={canEdit} onEdit={() => edit(r)} onDelete={() => del(r)} />)}</div>

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Recipes</h1>
          <p className="text-sm text-muted-foreground">
            Scripts that run once when a new server first boots (through cloud-init), picked on the order page.
          </p>
        </div>
        <Button onClick={() => setDraft({ ...empty, is_public: publisher })}><Plus className="h-4 w-4" aria-hidden="true" />New recipe</Button>
      </div>

      <section className="rounded-lg border bg-card text-card-foreground">
        <div className="border-b px-5 py-4">
          <h2 className="text-lg font-semibold">App catalog</h2>
          <p className="text-sm text-muted-foreground">
            {publisher ? "Published to every customer. They fill in the inputs when ordering." : "Ready-made apps from your provider, offered on the order page."}
          </p>
        </div>
        {list(catalog, publisher, publisher ? "Nothing is published yet. Add a recipe and tick Publish." : "Nothing is published yet.")}
      </section>

      <section className="rounded-lg border bg-card text-card-foreground">
        <div className="border-b px-5 py-4">
          <h2 className="text-lg font-semibold">Your recipes</h2>
          <p className="text-sm text-muted-foreground">Only you see these.</p>
        </div>
        {list(own, true, "None saved yet.")}
      </section>

      <Dialog open={!!draft} onOpenChange={(o) => !o && setDraft(null)}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{draft?.id ? "Edit recipe" : "New recipe"}</DialogTitle>
            <DialogDescription>A shell script (starting with #!) or a cloud-config document. It runs once, as root, on the first boot.</DialogDescription>
          </DialogHeader>
          {draft && (
            <div className="space-y-4">
              <div>
                <Label htmlFor="r-name">Name</Label>
                <Input id="r-name" value={draft.name} onChange={(e) => set({ name: e.target.value })} placeholder="WordPress" maxLength={100} />
              </div>
              <div>
                <Label htmlFor="r-desc">Description</Label>
                <Input id="r-desc" value={draft.description} onChange={(e) => set({ description: e.target.value })} placeholder="What it installs" maxLength={500} />
              </div>
              <div>
                <Label htmlFor="r-script">Script</Label>
                <textarea id="r-script" value={draft.script} onChange={(e) => set({ script: e.target.value })} placeholder={SAMPLE} rows={10}
                  className="w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-xs" spellCheck={false} />
              </div>
              <InputsEditor inputs={draft.inputs} onChange={(inputs) => set({ inputs })} />
              {publisher && (
                <label className="flex items-start gap-2 text-sm">
                  <input type="checkbox" className="mt-1" checked={draft.is_public} onChange={(e) => set({ is_public: e.target.checked })} />
                  <span>Publish to every customer<span className="block text-xs text-muted-foreground">It appears in the app catalog on their order page, script included.</span></span>
                </label>
              )}
            </div>
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDraft(null)}>Cancel</Button>
            <Button onClick={save} disabled={!draft?.name.trim() || !draft?.script.trim() || create.isPending || update.isPending}>
              {(create.isPending || update.isPending) && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
