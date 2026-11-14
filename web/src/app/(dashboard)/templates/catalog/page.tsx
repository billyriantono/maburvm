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
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { OSIcon } from "@/components/os-icon"
import { useCreateTemplate } from "@/lib/hooks/use-templates"
import type { OSType } from "@/types/template"

// CatalogEntry describes a ready-to-use image published by its vendor. The
// Linux ones ship cloud-init, which is how a VM gets its address and keys on
// first boot; a RouterOS one is set up from a script written to its disk.
interface CatalogEntry {
  id: string
  name: string
  version: string
  family: string
  arch: string
  sizeHint: string
  description: string
  url: string
  osType?: OSType
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
    description: "Noble Numbat, cloud image with cloud-init",
    url: "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
  },
  {
    id: "ubuntu-2204",
    name: "Ubuntu Server",
    version: "22.04 LTS",
    family: "Ubuntu",
    arch: "amd64",
    sizeHint: "~600 MB",
    description: "Jammy Jellyfish, cloud image with cloud-init",
    url: "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
  },
  {
    id: "debian-12",
    name: "Debian",
    version: "12",
    family: "Debian",
    arch: "amd64",
    sizeHint: "~350 MB",
    description: "Bookworm, generic cloud image (qcow2)",
    url: "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2",
  },
  {
    id: "debian-11",
    name: "Debian",
    version: "11",
    family: "Debian",
    arch: "amd64",
    sizeHint: "~350 MB",
    description: "Bullseye, generic cloud image (qcow2)",
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
    description: "Upstream of RHEL, generic cloud image (qcow2)",
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
  // The two RouterOS builds below were each started, configured, restarted and
  // signed in to on this platform before being listed. 6.49.21 to 6.49.23 are
  // newer but do not keep their settings as a VM, so version 6 stays on 6.49.20.
  {
    id: "routeros-7",
    name: "MikroTik RouterOS",
    version: "7.24.6",
    family: "MikroTik",
    arch: "x86_64",
    sizeHint: "~45 MB",
    description: "Cloud Hosted Router, long-term. Free licence: 1 Mbps per interface until the owner adds their own",
    url: "https://download.mikrotik.com/routeros/7.24.6/chr-7.24.6.img.zip",
    osType: "routeros",
  },
  {
    id: "routeros-6",
    name: "MikroTik RouterOS",
    version: "6.49.20",
    family: "MikroTik",
    arch: "x86_64",
    sizeHint: "~30 MB",
    description: "Cloud Hosted Router 6, long-term. Free licence: 1 Mbps per interface until the owner adds their own",
    url: "https://download.mikrotik.com/routeros/6.49.20/chr-6.49.20.img.zip",
    osType: "routeros",
  },
  // Windows cannot be downloaded: Microsoft does not allow an image to be
  // handed out. These register the file scripts/build-windows-template.sh
  // writes on a node, at the path its documentation uses.
  {
    id: "windows-2025",
    name: "Windows Server",
    version: "2025",
    family: "Windows",
    arch: "x86_64",
    sizeHint: "~7 GB",
    description: "Standard, with desktop. Built on your nodes; no licence included, owners activate with their own key",
    url: "/var/lib/libvirt/templates/windows-2025.qcow2",
    osType: "windows",
  },
  {
    id: "windows-2022",
    name: "Windows Server",
    version: "2022",
    family: "Windows",
    arch: "x86_64",
    sizeHint: "~5 GB",
    description: "Standard, with desktop. Built on your nodes; no licence included, owners activate with their own key",
    url: "/var/lib/libvirt/templates/windows-2022.qcow2",
    osType: "windows",
  },
]

type AddState = "idle" | "adding" | "added" | "error"

export default function TemplateCatalogPage() {
  const router = useRouter()
  const createTemplate = useCreateTemplate()
  const [states, setStates] = useState<Record<string, AddState>>({})

  const handleAdd = async (entry: CatalogEntry) => {
    setStates((s) => ({ ...s, [entry.id]: "adding" }))
    try {
      await createTemplate.mutateAsync({
        name: `${entry.name} ${entry.version}`,
        version: entry.version,
        file_url: entry.url,
        description: `${entry.description} (${entry.arch})`,
        os_type: entry.osType ?? "linux",
      })
      setStates((s) => ({ ...s, [entry.id]: "added" }))
      toast.success(`${entry.name} ${entry.version} added to library`)
    } catch (err) {
      const message = (err as Error).message || "Failed to add template"
      // Treat an existing template as a soft success so re-adding is harmless.
      if (/exist/i.test(message)) {
        setStates((s) => ({ ...s, [entry.id]: "added" }))
        toast.info(`${entry.name} ${entry.version} is already in your library`)
      } else {
        setStates((s) => ({ ...s, [entry.id]: "error" }))
        toast.error(message)
      }
    }
  }

  return (
    <div className="max-w-6xl mx-auto">
      {/* Header */}
      <div className="mb-6">
        <Link href="/templates" className="inline-flex items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors mb-3">
          <ArrowLeft className="w-4 h-4" />
          Back to templates
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">Template catalog</h1>
        <p className="text-muted-foreground text-sm mt-1">
          Images published by their vendors. Add one to your library, then build VMs from it.
        </p>
      </div>

      {/* How it works */}
      <div className="bg-muted border rounded-lg p-4 mb-6 flex items-start gap-3">
        <Info className="w-5 h-5 mt-0.5 shrink-0 text-muted-foreground" />
        <div className="text-sm text-muted-foreground">
          <p className="font-medium text-foreground text-xs mb-1">How it works</p>
          Adding a template records where its image is. The first time a VM is built from it, the node
          downloads the image and keeps it, which can take a few minutes for a large one; later builds reuse it. Each VM gets its address, name and password set for it on first start.
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
                variant={state === "added" ? "success" : "outline"}
                onClick={() => handleAdd(entry)}
                disabled={state === "adding" || state === "added"}
                className="mt-4 w-full gap-2"
              >
                {state === "adding" && <><Loader2 className="w-4 h-4 animate-spin" /> Adding</>}
                {state === "added" && <><Check className="w-4 h-4" /> In library</>}
                {state === "error" && <><AlertCircle className="w-4 h-4" /> Retry</>}
                {state === "idle" && <><Download className="w-4 h-4" /> Add to library</>}
              </Button>
            </div>
          )
        })}
      </div>

      {/* The Windows entries register a file the operator builds; say how. */}
      <div className="mt-6 rounded-lg border bg-card p-5 text-sm text-card-foreground">
        <h2 className="font-semibold">Before adding Windows Server</h2>
        <p className="mt-1 max-w-3xl text-muted-foreground">
          Microsoft does not allow a Windows image to be shared, so there is nothing to download. Build the image on every
          node that should offer it (about fifteen minutes after the download, unattended), then add the entry above:
        </p>
        <pre className="mt-3 overflow-x-auto rounded-md border bg-muted px-3 py-2 font-mono text-xs text-foreground">
          scripts/build-windows-template.sh --version 2025 --out /var/lib/libvirt/templates/windows-2025.qcow2
        </pre>
        <p className="mt-3 max-w-3xl text-muted-foreground">
          A VM created on a node that does not have the file fails with a message naming the missing path.
          For another edition or your own licensed media, build with the script&apos;s options and use{" "}
          <Link href="/templates/new" className="font-medium text-foreground underline">Add from URL</Link>.
        </p>
      </div>

      {/* Footer actions */}
      <div className="mt-8 flex items-center justify-between border-t pt-6">
        <p className="text-xs text-muted-foreground">
          Need a custom image? Use{" "}
          <Link href="/templates/new" className="font-medium text-foreground underline">Add from URL</Link>.
        </p>
        <Button variant="outline" onClick={() => router.push("/templates")} className="gap-2">
          View library
        </Button>
      </div>

    </div>
  )
}
