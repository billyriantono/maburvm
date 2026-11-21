"use client"

import { Input } from "@/components/ui/input"
import type { Firmware } from "@/types/template"

export interface ImageIntegrity {
  checksumSha256: string
  checksumUrl: string
  skipChecksum: boolean
  allowHttp: boolean
  autoRefresh: boolean
  firmware: Firmware
}

export const emptyIntegrity: ImageIntegrity = {
  checksumSha256: "", checksumUrl: "", skipChecksum: false, allowHttp: false, autoRefresh: false, firmware: "bios",
}

const SHA256 = /^[0-9a-f]{64}$/i

// integrityProblem says what is missing for an image at url, or "" when the
// fields are complete. The panel checks the same rules.
export function integrityProblem(url: string, v: ImageIntegrity, requireChecksum = true): string {
  const remote = /^https?:\/\//i.test(url.trim())
  if (/^http:\/\//i.test(url.trim()) && !v.allowHttp) {
    return "The address is plain http. Use https, or allow http for this image below."
  }
  if (v.checksumSha256.trim() && !SHA256.test(v.checksumSha256.trim())) {
    return "A sha256 is 64 hexadecimal characters."
  }
  if (v.checksumUrl.trim() && !/^https?:\/\//i.test(v.checksumUrl.trim())) {
    return "The checksum list must be an http(s) address."
  }
  if (requireChecksum && remote && !v.checksumSha256.trim() && !v.checksumUrl.trim() && !v.skipChecksum) {
    return "Give the image's sha256 or a checksum list, or confirm the publisher gives none."
  }
  if (v.autoRefresh && !v.checksumUrl.trim()) {
    return "Refreshing by itself needs a checksum list to watch."
  }
  return ""
}

// ImageIntegrityFields edits how the nodes check an image: its sha256 or a
// checksum list, plain http, daily refresh, and the firmware it boots with.
export function ImageIntegrityFields({ value, onChange, idPrefix, showFirmware = true, allowSkip = true }: {
  value: ImageIntegrity
  onChange: (v: ImageIntegrity) => void
  idPrefix: string
  showFirmware?: boolean
  allowSkip?: boolean
}) {
  const set = (patch: Partial<ImageIntegrity>) => onChange({ ...value, ...patch })
  return (
    <div className="space-y-4">
      <div>
        <label htmlFor={`${idPrefix}-sha256`} className="block text-sm font-medium mb-2">sha256</label>
        <Input id={`${idPrefix}-sha256`} value={value.checksumSha256} onChange={(e) => set({ checksumSha256: e.target.value })}
          placeholder="64 hexadecimal characters" className="font-mono" spellCheck={false} />
      </div>
      <div>
        <label htmlFor={`${idPrefix}-checksum-url`} className="block text-sm font-medium mb-2">Or a checksum list</label>
        <Input id={`${idPrefix}-checksum-url`} inputMode="url" value={value.checksumUrl} onChange={(e) => set({ checksumUrl: e.target.value })}
          placeholder="https://example.com/images/SHA256SUMS" />
        <p className="text-xs text-muted-foreground mt-2">
          A SHA256SUMS or CHECKSUM file listing the image by its file name. The panel reads the checksum from it now, and
          again when you refresh the image. Every node refuses a download that does not match.
        </p>
      </div>
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-1" checked={value.autoRefresh} onChange={(e) => set({ autoRefresh: e.target.checked })} />
        <span>Refresh every day: when the checksum list changes, the nodes fetch the new image. VMs built from the old one are not touched.</span>
      </label>
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-1" checked={value.allowHttp} onChange={(e) => set({ allowHttp: e.target.checked })} />
        <span>Allow a plain http address. Anyone on the way can change what is downloaded; the checksum is then what protects it.</span>
      </label>
      {allowSkip && (
        <label className="flex items-start gap-2 text-sm">
          <input type="checkbox" className="mt-1" checked={value.skipChecksum} onChange={(e) => set({ skipChecksum: e.target.checked })} />
          <span>The publisher gives no checksum. The nodes then cannot tell a corrupt or changed download.</span>
        </label>
      )}
      {showFirmware && (
        <div>
          <label htmlFor={`${idPrefix}-firmware`} className="block text-sm font-medium mb-2">Boot firmware</label>
          <select id={`${idPrefix}-firmware`} value={value.firmware} onChange={(e) => set({ firmware: e.target.value as Firmware })}
            className="h-10 w-full max-w-xs rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2">
            <option value="bios">BIOS</option>
            <option value="uefi">UEFI</option>
          </select>
          {value.firmware === "uefi" && (
            <p className="text-xs text-muted-foreground mt-2">
              The node agents cannot boot UEFI guests yet, so VMs cannot be created from this template until they can.
            </p>
          )}
        </div>
      )}
    </div>
  )
}
