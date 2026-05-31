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
    <div className={`fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo ${
      type === "success" ? "bg-success text-black" : "bg-danger text-white"
    }`}>
      <p className="font-bold uppercase text-sm">{message}</p>
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
          <Button variant="ghost" size="sm" className="border-2 border-black">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">Add Template from URL</h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
            Register a custom OS image by URL
          </p>
        </div>
      </div>

      {/* Catalog hint */}
      <div className="bg-secondary/20 border-2 border-black p-4 mb-6 flex items-start gap-3">
        <Info className="w-5 h-5 mt-0.5 shrink-0" />
        <div className="text-sm font-medium">
          Want a ready-made OS? Use{" "}
          <Link href="/templates/catalog" className="font-bold text-black underline">Browse Catalog</Link>{" "}
          for curated cloud images. Use this page only for a custom image hosted at a URL.
        </div>
      </div>

      <form onSubmit={handleSubmit}>
        {/* Basic Info */}
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2">
            <FileArchive className="w-5 h-5" />
            Basic Information
          </h2>

          <div className="space-y-4">
            <div>
              <label htmlFor="template-name" className="block text-sm font-bold uppercase mb-2">
                Template Name <span className="text-danger">*</span>
              </label>
              <Input
                id="template-name"
                type="text"
                placeholder="e.g., Ubuntu Server 24.04"
                value={name}
                onChange={(e) => setName(e.target.value)}
                className={`border-2 border-black ${errors.name ? "border-danger" : ""}`}
              />
              {errors.name && <p className="text-danger text-xs font-bold mt-1">{errors.name}</p>}
            </div>

            <div>
              <label htmlFor="template-version" className="block text-sm font-bold uppercase mb-2">
                Version <span className="text-danger">*</span>
              </label>
              <Input
                id="template-version"
                type="text"
                placeholder="e.g., 24.04"
                value={version}
                onChange={(e) => setVersion(e.target.value)}
                className={`border-2 border-black w-40 ${errors.version ? "border-danger" : ""}`}
              />
              {errors.version && <p className="text-danger text-xs font-bold mt-1">{errors.version}</p>}
            </div>

            <div>
              <label htmlFor="template-description" className="block text-sm font-bold uppercase mb-2">
                Description
              </label>
              <textarea
                id="template-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Optional description for this template..."
                rows={3}
                className="w-full px-4 py-3 border-2 border-black font-medium focus:outline-none focus:shadow-neo-sm resize-none"
              />
            </div>
          </div>
        </div>

        {/* Image Source */}
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2">
            <Cloud className="w-5 h-5" />
            Image Source
          </h2>

          <label htmlFor="template-url" className="block text-sm font-bold uppercase mb-2">
            Image URL <span className="text-danger">*</span>
          </label>
          <div className="relative">
            <LinkIcon className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-600" />
            <Input
              id="template-url"
              type="url"
              placeholder="https://example.com/image.qcow2"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              className={`pl-10 border-2 border-black ${errors.url ? "border-danger" : ""}`}
            />
          </div>
          {errors.url && (
            <p className="text-danger text-xs font-bold mt-2 flex items-center gap-1">
              <AlertCircle className="w-4 h-4" />
              {errors.url}
            </p>
          )}
          <p className="text-xs text-gray-500 mt-2">
            Point to a qcow2/img cloud image. The target node downloads and caches it on the first VM build
            (not now), so creation is instant. Images with cloud-init get automatic IP/SSH configuration.
          </p>
        </div>

        {/* Submit */}
        <div className="flex gap-4 justify-end">
          <Link href="/templates">
            <Button variant="ghost" type="button" className="border-2 border-black">
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
