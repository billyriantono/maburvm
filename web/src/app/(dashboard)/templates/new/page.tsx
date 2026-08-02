"use client"

import { useState, useEffect } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import {
  ArrowLeft,
  Link as LinkIcon,
  FileArchive,
  Loader2,
  AlertCircle,
  Cloud,
  Info,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useCreateTemplate } from "@/lib/hooks/use-templates"

interface FormErrors {
  name?: string
  version?: string
  url?: string
}

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  useEffect(() => {
    const timer = setTimeout(onClose, 3000)
    return () => clearTimeout(timer)
  }, [onClose])
  return (
    <div className={`fixed bottom-4 right-4 z-50 rounded-md border px-6 py-4 shadow-md ${
      type === "success" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : "bg-destructive text-destructive-foreground border-destructive"
    }`}>
      <p className="text-sm font-medium">{message}</p>
    </div>
  )
}

export default function NewTemplatePage() {
  const router = useRouter()
  const createTemplate = useCreateTemplate()

  const [name, setName] = useState("")
  const [version, setVersion] = useState("")
  const [description, setDescription] = useState("")
  const [url, setUrl] = useState("")
  const [errors, setErrors] = useState<FormErrors>({})
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)

  const validateForm = (): boolean => {
    const next: FormErrors = {}
    if (!name.trim()) next.name = "Template name is required"
    else if (name.trim().length > 100) next.name = "Name must be 100 characters or fewer"
    if (!version.trim()) next.version = "Version is required"
    if (!url.trim()) next.url = "Image URL is required"
    else if (!/^https?:\/\/.+/i.test(url.trim())) next.url = "Enter a valid http(s) URL"
    setErrors(next)
    return Object.keys(next).length === 0
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateForm()) return

    try {
      await createTemplate.mutateAsync({
        name: name.trim(),
        version: version.trim(),
        file_url: url.trim(),
        description: description.trim() || undefined,
      })
      setToast({ message: `Template "${name}" added`, type: "success" })
      setTimeout(() => router.push("/templates"), 1200)
    } catch (err) {
      const message = (err as Error).message || "Failed to create template"
      if (/exist/i.test(message)) {
        setToast({ message: "A template with this name and version already exists", type: "error" })
      } else {
        setToast({ message, type: "error" })
      }
    }
  }

  return (
    <div className="max-w-3xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/templates">
          <Button variant="outline" size="icon">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Add Template from URL</h1>
          <p className="text-muted-foreground text-sm mt-1">
            Register a custom OS image by URL
          </p>
        </div>
      </div>

      {/* Catalog hint */}
      <div className="bg-muted border rounded-lg p-4 mb-6 flex items-start gap-3">
        <Info className="w-5 h-5 mt-0.5 shrink-0 text-muted-foreground" />
        <div className="text-sm text-muted-foreground">
          Want a ready-made OS? Use{" "}
          <Link href="/templates/catalog" className="font-medium text-foreground underline">Browse Catalog</Link>{" "}
          for curated cloud images. Use this page only for a custom image hosted at a URL.
        </div>
      </div>

      <form onSubmit={handleSubmit}>
        {/* Basic Info */}
        <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
          <h2 className="text-base font-semibold mb-4 flex items-center gap-2">
            <FileArchive className="w-5 h-5" />
            Basic Information
          </h2>

          <div className="space-y-4">
            <div>
              <label htmlFor="template-name" className="block text-sm font-medium mb-2">
                Template Name <span className="text-destructive">*</span>
              </label>
              <Input
                id="template-name"
                type="text"
                placeholder="e.g., Ubuntu Server 24.04"
                value={name}
                onChange={(e) => setName(e.target.value)}
                className={errors.name ? "border-destructive" : ""}
              />
              {errors.name && <p className="text-destructive text-xs mt-1">{errors.name}</p>}
            </div>

            <div>
              <label htmlFor="template-version" className="block text-sm font-medium mb-2">
                Version <span className="text-destructive">*</span>
              </label>
              <Input
                id="template-version"
                type="text"
                placeholder="e.g., 24.04"
                value={version}
                onChange={(e) => setVersion(e.target.value)}
                className={`w-40 ${errors.version ? "border-destructive" : ""}`}
              />
              {errors.version && <p className="text-destructive text-xs mt-1">{errors.version}</p>}
            </div>

            <div>
              <label htmlFor="template-description" className="block text-sm font-medium mb-2">
                Description
              </label>
              <textarea
                id="template-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Optional description for this template..."
                rows={3}
                className="w-full px-3 py-2 rounded-md border border-input bg-background text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 resize-none"
              />
            </div>
          </div>
        </div>

        {/* Image Source */}
        <div className="bg-card text-card-foreground border rounded-lg p-6 shadow-sm mb-6">
          <h2 className="text-base font-semibold mb-4 flex items-center gap-2">
            <Cloud className="w-5 h-5" />
            Image Source
          </h2>

          <label htmlFor="template-url" className="block text-sm font-medium mb-2">
            Image URL <span className="text-destructive">*</span>
          </label>
          <div className="relative">
            <LinkIcon className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
            <Input
              id="template-url"
              type="url"
              placeholder="https://example.com/image.qcow2"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              className={`pl-10 ${errors.url ? "border-destructive" : ""}`}
            />
          </div>
          {errors.url && (
            <p className="text-destructive text-xs mt-2 flex items-center gap-1">
              <AlertCircle className="w-4 h-4" />
              {errors.url}
            </p>
          )}
          <p className="text-xs text-muted-foreground mt-2">
            Point to a qcow2/img cloud image. The target node downloads and caches it on the first VM build
            (not now), so creation is instant. Images with cloud-init get automatic IP/SSH configuration.
          </p>
        </div>

        {/* Submit */}
        <div className="flex gap-4 justify-end">
          <Link href="/templates">
            <Button variant="outline" type="button">
              Cancel
            </Button>
          </Link>
          <Button type="submit" disabled={createTemplate.isPending} className="gap-2">
            {createTemplate.isPending && <Loader2 className="w-4 h-4 animate-spin" />}
            Add Template
          </Button>
        </div>
      </form>

      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
