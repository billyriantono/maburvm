"use client"

import { useState } from "react"
import { toast } from "sonner"
import {
  ScrollText,
  Plus,
  Pencil,
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
import { useRecipes, useCreateRecipe, useUpdateRecipe, useDeleteRecipe } from "@/lib/hooks/use-recipes"
import type { Recipe } from "@/types/recipe"

function formatDate(value?: string): string {
  if (!value) return "—"
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString()
}

const SAMPLE_SCRIPT = "#!/bin/bash\napt-get update && apt-get install -y nginx"

export default function RecipesSettingsPage() {
  const { data: recipes, isLoading, error } = useRecipes()
  const createRecipe = useCreateRecipe()
  const updateRecipe = useUpdateRecipe()
  const deleteRecipe = useDeleteRecipe()

  const [editing, setEditing] = useState<Recipe | null>(null)
  const [showForm, setShowForm] = useState(false)
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [script, setScript] = useState("")
  const [deleteTarget, setDeleteTarget] = useState<Recipe | null>(null)

  const openCreate = () => {
    setEditing(null)
    setName("")
    setDescription("")
    setScript("")
    setShowForm(true)
  }

  const openEdit = (r: Recipe) => {
    setEditing(r)
    setName(r.name)
    setDescription(r.description)
    setScript(r.script)
    setShowForm(true)
  }

  const handleSave = async () => {
    const n = name.trim()
    const s = script.trim()
    if (!n || !s) {
      toast.error("Name and script are required")
      return
    }
    const payload = { name: n, description: description.trim() || undefined, script }
    try {
      if (editing) {
        await updateRecipe.mutateAsync({ id: editing.id, data: payload })
        toast.success("Recipe updated")
      } else {
        await createRecipe.mutateAsync(payload)
        toast.success("Recipe created")
      }
      setShowForm(false)
    } catch (err) {
      toast.error(editing ? "Failed to update recipe" : "Failed to create recipe", {
        description: (err as Error).message,
      })
    }
  }

  const handleDelete = async () => {
    if (!deleteTarget) return
    try {
      await deleteRecipe.mutateAsync(deleteTarget.id)
      toast.success("Recipe deleted", { description: `"${deleteTarget.name}" removed.` })
      setDeleteTarget(null)
    } catch (err) {
      toast.error("Failed to delete recipe", { description: (err as Error).message })
    }
  }

  const saving = createRecipe.isPending || updateRecipe.isPending

  return (
    <div className="max-w-4xl mx-auto">
      {/* Header */}
      <div className="mb-8 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">Recipes</h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
            First-boot scripts you can apply when creating a VM
          </p>
        </div>
        <Button onClick={openCreate}>
          <Plus className="w-4 h-4 mr-2" />
          New Recipe
        </Button>
      </div>

      {/* List */}
      <Card>
        <CardHeader className="border-b-2 border-black">
          <CardTitle className="flex items-center gap-2">
            <ScrollText className="w-5 h-5" />
            Your Recipes
          </CardTitle>
          <CardDescription>
            Each recipe runs once on first boot via cloud-init (the guest must support cloud-init).
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {isLoading ? (
            <div className="p-6 space-y-3">
              <Skeleton className="h-16 border-2 border-black" />
              <Skeleton className="h-16 border-2 border-black" />
            </div>
          ) : error ? (
            <div className="p-12 text-center">
              <AlertCircle className="w-12 h-12 text-danger mx-auto mb-3" />
              <p className="font-bold uppercase">Failed to load recipes</p>
              <p className="text-sm text-gray-500 mt-1">{(error as Error).message}</p>
            </div>
          ) : !recipes || recipes.length === 0 ? (
            <div className="p-12 text-center">
              <ScrollText className="w-12 h-12 text-gray-300 mx-auto mb-3" />
              <p className="font-bold uppercase">No recipes yet</p>
              <p className="text-sm text-gray-500 mt-1 mb-4">
                Save a startup script once, then apply it to any new VM.
              </p>
              <Button onClick={openCreate}>
                <Plus className="w-4 h-4 mr-2" />
                New Recipe
              </Button>
            </div>
          ) : (
            <ul className="divide-y-2 divide-black">
              {recipes.map((r) => (
                <li key={r.id} className="p-4 flex items-center justify-between gap-4">
                  <div className="min-w-0">
                    <span className="font-bold text-black truncate block">{r.name}</span>
                    {r.description && (
                      <p className="text-sm text-gray-600 truncate">{r.description}</p>
                    )}
                    <p className="text-xs text-gray-500 mt-1">Updated {formatDate(r.updated_at)}</p>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <Button variant="ghost" size="sm" className="border-2 border-black" onClick={() => openEdit(r)}>
                      <Pencil className="w-4 h-4" />
                    </Button>
                    <Button variant="destructive" size="sm" onClick={() => setDeleteTarget(r)}>
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

      {/* Create/Edit dialog */}
      <Dialog open={showForm} onOpenChange={(open) => !open && setShowForm(false)}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              {editing ? <Pencil className="w-5 h-5" /> : <Plus className="w-5 h-5" />}
              {editing ? "Edit Recipe" : "New Recipe"}
            </DialogTitle>
            <DialogDescription>
              The script is injected as cloud-init user-data and runs once on first boot.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div>
              <label htmlFor="recipe-name" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                Name
              </label>
              <Input
                id="recipe-name"
                placeholder="e.g. install-nginx"
                value={name}
                onChange={(e) => setName(e.target.value)}
                maxLength={100}
              />
            </div>
            <div>
              <label htmlFor="recipe-desc" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                Description (optional)
              </label>
              <Input
                id="recipe-desc"
                placeholder="What this recipe does"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                maxLength={500}
              />
            </div>
            <div>
              <label htmlFor="recipe-script" className="block text-sm font-bold uppercase tracking-wider text-gray-500 mb-2">
                Script
              </label>
              <textarea
                id="recipe-script"
                placeholder={SAMPLE_SCRIPT}
                value={script}
                onChange={(e) => setScript(e.target.value)}
                rows={12}
                className="w-full border-2 border-black p-3 font-mono text-xs resize-y focus:outline-none focus:ring-2 focus:ring-primary"
              />
              <p className="text-xs text-gray-500 mt-1">
                Start with a shebang (e.g. <code>#!/bin/bash</code>). Runs as root on first boot.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setShowForm(false)}>
              Cancel
            </Button>
            <Button onClick={handleSave} disabled={saving}>
              {saving ? (
                <><Loader2 className="w-4 h-4 mr-2 animate-spin" />Saving...</>
              ) : (
                <>{editing ? <Pencil className="w-4 h-4 mr-2" /> : <Plus className="w-4 h-4 mr-2" />}{editing ? "Save Changes" : "Create Recipe"}</>
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
              Delete Recipe
            </DialogTitle>
            <DialogDescription>
              Remove &quot;{deleteTarget?.name}&quot;? This won&apos;t affect VMs already created with it.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteTarget(null)}>Cancel</Button>
            <Button variant="destructive" onClick={handleDelete} disabled={deleteRecipe.isPending}>
              {deleteRecipe.isPending ? <Loader2 className="w-4 h-4 mr-2 animate-spin" /> : <Trash2 className="w-4 h-4 mr-2" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
