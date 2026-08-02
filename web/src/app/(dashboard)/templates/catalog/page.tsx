"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import {
  ArrowLeft,
  Check,
  Download,
  Loader2,
  AlertCircle,
  HardDrive,
  Info,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { OSIcon } from "@/components/os-icon"
import { useCreateTemplate } from "@/lib/hooks/use-templates"

// CatalogEntry describes a curated, ready-to-use OS cloud image. Each image
// ships with cloud-init pre-installed, which is what lets MaburVM inject the
// assigned IP/SSH config into the guest at first boot.
interface CatalogEntry {
  id: string
  name: string
  version: string
  family: string
  arch: string
  sizeHint: string
  description: string
  url: string
}

// Stable "latest"/"current" image URLs from each distro's official cloud-image
// repository. These symlinks always resolve to the newest patched build.
const OS_CATALOG: CatalogEntry[] = [
  {
    id: "ubuntu-2404",
    name: "Ubuntu Server",
    version: "24.04 LTS",
    family: "Ubuntu",
    arch: "amd64",
    sizeHint: "~600 MB",
    description: "Noble Numbat — cloud image with cloud-init",
    url: "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
  },
  {
    id: "ubuntu-2204",
    name: "Ubuntu Server",
    version: "22.04 LTS",
    family: "Ubuntu",
    arch: "amd64",
    sizeHint: "~600 MB",
    description: "Jammy Jellyfish — cloud image with cloud-init",
    url: "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
  },
  {
    id: "debian-12",
    name: "Debian",
    version: "12",
    family: "Debian",
    arch: "amd64",
    sizeHint: "~350 MB",
    description: "Bookworm — generic cloud image (qcow2)",
    url: "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2",
  },
  {
    id: "debian-11",
    name: "Debian",
    version: "11",
    family: "Debian",
    arch: "amd64",
    sizeHint: "~350 MB",
    description: "Bullseye — generic cloud image (qcow2)",
    url: "https://cloud.debian.org/images/cloud/bullseye/latest/debian-11-genericcloud-amd64.qcow2",
  },
  {
    id: "rocky-9",
    name: "Rocky Linux",
    version: "9",
    family: "RHEL",
    arch: "x86_64",
    sizeHint: "~600 MB",
    description: "Generic cloud image (qcow2), RHEL-compatible",
    url: "https://download.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud.latest.x86_64.qcow2",
  },
  {
    id: "alma-9",
    name: "AlmaLinux",
    version: "9",
    family: "RHEL",
    arch: "x86_64",
    sizeHint: "~600 MB",
    description: "Generic cloud image (qcow2), RHEL-compatible",
    url: "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2",
  },
  {
    id: "centos-stream-9",
    name: "CentOS Stream",
    version: "9",
    family: "RHEL",
    arch: "x86_64",
    sizeHint: "~800 MB",
    description: "Upstream of RHEL — generic cloud image (qcow2)",
    url: "https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2",
  },
  {
    id: "fedora-41",
    name: "Fedora Cloud",
    version: "41",
    family: "Fedora",
    arch: "x86_64",
    sizeHint: "~500 MB",
    description: "Base cloud image (qcow2)",
    url: "https://download.fedoraproject.org/pub/fedora/linux/releases/41/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-41-1.4.x86_64.qcow2",
  },
  {
    id: "arch",
    name: "Arch Linux",
    version: "rolling",
    family: "Arch",
    arch: "x86_64",
    sizeHint: "~550 MB",
    description: "Latest rolling cloud image (qcow2)",
    url: "https://geo.mirror.pkgbuild.com/images/latest/Arch-Linux-x86_64-cloudimg.qcow2",
  },
  {
    id: "opensuse-leap-156",
    name: "openSUSE Leap",
    version: "15.6",
    family: "SUSE",
    arch: "x86_64",
    sizeHint: "~500 MB",
    description: "NoCloud image (qcow2)",
    url: "https://download.opensuse.org/repositories/Cloud:/Images:/Leap_15.6/images/openSUSE-Leap-15.6.x86_64-NoCloud.qcow2",
  },
]

type AddState = "idle" | "adding" | "added" | "error"

function Toast({ message, type, onClose }: { message: string; type: "success" | "error"; onClose: () => void }) {
  return (
    <div className={`fixed bottom-4 right-4 z-50 rounded-md border px-6 py-4 shadow-md ${
      type === "success" ? "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900" : "bg-destructive text-destructive-foreground border-destructive"
    }`}>
      <div className="flex items-center gap-3">
        <p className="text-sm font-medium">{message}</p>
        <button onClick={onClose} className="font-medium">✕</button>
      </div>
    </div>
  )
}

export default function TemplateCatalogPage() {
  const router = useRouter()
  const createTemplate = useCreateTemplate()
  const [states, setStates] = useState<Record<string, AddState>>({})
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)

  const handleAdd = async (entry: CatalogEntry) => {
    setStates((s) => ({ ...s, [entry.id]: "adding" }))
    try {
      await createTemplate.mutateAsync({
        name: `${entry.name} ${entry.version}`,
        version: entry.version,
        file_url: entry.url,
        description: `${entry.description} • ${entry.arch}`,
      })
      setStates((s) => ({ ...s, [entry.id]: "added" }))
      setToast({ message: `${entry.name} ${entry.version} added to library`, type: "success" })
    } catch (err) {
      const message = (err as Error).message || "Failed to add template"
      // Treat an existing template as a soft success so re-adding is harmless.
      if (/exist/i.test(message)) {
        setStates((s) => ({ ...s, [entry.id]: "added" }))
        setToast({ message: `${entry.name} ${entry.version} is already in your library`, type: "success" })
      } else {
        setStates((s) => ({ ...s, [entry.id]: "error" }))
        setToast({ message, type: "error" })
      }
    }
  }

  return (
    <div className="max-w-6xl mx-auto">
      {/* Header */}
      <div className="mb-6">
        <Link href="/templates" className="inline-flex items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors mb-3">
          <ArrowLeft className="w-4 h-4" />
          Back to Templates
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">OS Template Catalog</h1>
        <p className="text-muted-foreground text-sm mt-1">
          Curated cloud images — add one to your library, then build VMs from it
        </p>
      </div>

      {/* How it works */}
      <div className="bg-muted border rounded-lg p-4 mb-6 flex items-start gap-3">
        <Info className="w-5 h-5 mt-0.5 shrink-0 text-muted-foreground" />
        <div className="text-sm text-muted-foreground">
          <p className="font-medium text-foreground text-xs mb-1">How it works</p>
          Adding a template registers its image URL. The first time you build a VM from it,
          the target node downloads and caches the image (one-time, may take a few minutes for large images);
          later builds reuse the cache. All images include cloud-init for automatic IP/SSH configuration.
        </div>
      </div>

      {/* Catalog grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {OS_CATALOG.map((entry) => {
          const state = states[entry.id] || "idle"
          return (
            <div key={entry.id} className="bg-card text-card-foreground border rounded-lg shadow-sm p-5 flex flex-col">
              <div className="flex items-start justify-between mb-3">
                <div className="w-12 h-12 bg-muted rounded-md flex items-center justify-center border">
                  <OSIcon name={entry.name} className="w-7 h-7" />
                </div>
                <span className="px-2 py-0.5 rounded-md bg-muted text-muted-foreground border text-xs font-medium">
                  {entry.arch}
                </span>
              </div>
              <h3 className="font-semibold text-lg leading-tight">{entry.name}</h3>
              <div className="flex items-center gap-2 mt-1">
                <span className="px-2 py-0.5 rounded-md bg-muted text-muted-foreground border text-xs font-medium">
                  {entry.version}
                </span>
                <span className="text-xs text-muted-foreground flex items-center gap-1">
                  <HardDrive className="w-3 h-3" /> {entry.sizeHint}
                </span>
              </div>
              <p className="text-sm text-muted-foreground mt-3 flex-1">{entry.description}</p>

              <Button
                variant={state === "added" ? "success" : "default"}
                onClick={() => handleAdd(entry)}
                disabled={state === "adding" || state === "added"}
                className="mt-4 w-full gap-2"
              >
                {state === "adding" && <><Loader2 className="w-4 h-4 animate-spin" /> Adding…</>}
                {state === "added" && <><Check className="w-4 h-4" /> In Library</>}
                {state === "error" && <><AlertCircle className="w-4 h-4" /> Retry</>}
                {state === "idle" && <><Download className="w-4 h-4" /> Add to Library</>}
              </Button>
            </div>
          )
        })}
      </div>

      {/* Footer actions */}
      <div className="mt-8 flex items-center justify-between border-t pt-6">
        <p className="text-xs text-muted-foreground">
          Need a custom image? Use{" "}
          <Link href="/templates/new" className="font-medium text-foreground underline">Add from URL</Link>.
        </p>
        <Button variant="outline" onClick={() => router.push("/templates")} className="gap-2">
          View Library
        </Button>
      </div>

      {toast && <Toast message={toast.message} type={toast.type} onClose={() => setToast(null)} />}
    </div>
  )
}
