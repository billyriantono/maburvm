"use client"

import { Check } from "lucide-react"
import { OSLogoTile, osIdentity } from "@/components/os-icon"
import { cn } from "@/lib/utils"
import type { OSTemplate } from "@/types"

// The part of a template that tells versions of one distribution apart:
// its version field, or failing that whatever follows the distribution name.
function versionOf(t: OSTemplate, family: string): string {
  const v = (t.version ?? "").trim()
  if (v) return v
  const rest = t.name.replace(new RegExp(`^${family.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*`, "i"), "").trim()
  // Nothing left means the name is all there is: no version to show.
  return rest
}

interface Family {
  key: string
  label: string
  /** Newest first. */
  versions: { template: OSTemplate; version: string }[]
}

function groupByFamily(templates: OSTemplate[]): Family[] {
  const families = new Map<string, Family>()
  for (const t of templates) {
    const id = osIdentity(t.name)
    const family = families.get(id.key) ?? { key: id.key, label: id.label, versions: [] }
    family.versions.push({ template: t, version: versionOf(t, id.label) })
    families.set(id.key, family)
  }
  for (const f of families.values()) {
    f.versions.sort((a, b) => b.version.localeCompare(a.version, undefined, { numeric: true }))
  }
  return [...families.values()]
}

// OSPicker shows one card per distribution, with its versions as choices inside
// the card. Ten templates of three distributions are three things to scan, not
// ten, and the logo does the recognising that a list of names cannot.
export function OSPicker({
  templates,
  value,
  onChange,
  labelledBy,
}: {
  templates: OSTemplate[]
  value: string
  onChange: (templateId: string) => void
  /** id of the heading that names this group. */
  labelledBy?: string
}) {
  const families = groupByFamily(templates)

  return (
    <div role="group" aria-labelledby={labelledBy} className="grid grid-cols-2 gap-3 md:grid-cols-3 2xl:grid-cols-4">
      {families.map((family) => {
        const chosen = family.versions.find((v) => v.template.id === value)
        const selected = !!chosen
        const single = family.versions.length === 1
        // Clicking the card picks the newest version, or keeps the one chosen.
        const primary = chosen ?? family.versions[0]

        return (
          <div
            key={family.key}
            className={cn(
              // The focus ring is drawn on the whole card, not on the button
              // inside it, which only covers the top half when versions follow.
              "relative flex flex-col rounded-lg border bg-card transition-[border-color,box-shadow,background-color] duration-150 [&:has(>button:focus-visible)]:ring-2 [&:has(>button:focus-visible)]:ring-ring [&:has(>button:focus-visible)]:ring-offset-2",
              selected ? "border-primary shadow-[0_0_0_1px_hsl(var(--primary))]" : "hover:border-foreground/30"
            )}
          >
            <button
              type="button"
              aria-pressed={selected}
              onClick={() => onChange(primary.template.id)}
              className="flex flex-1 flex-col items-start gap-3 rounded-lg p-4 text-left transition-transform duration-150 active:scale-[0.98] focus-visible:outline-none"
            >
              <OSLogoTile name={family.label} />
              <span className="block">
                <span className="block text-sm font-semibold leading-tight">{family.label}</span>
                {single && family.versions[0].version && (
                  <span className="mt-1 block text-xs text-muted-foreground">{family.versions[0].version}</span>
                )}
              </span>
            </button>

            {selected && (
              <span className="pointer-events-none absolute right-3 top-3 flex h-5 w-5 items-center justify-center rounded-full bg-primary text-primary-foreground animate-in zoom-in-50 duration-200">
                <Check className="h-3 w-3" aria-hidden="true" />
              </span>
            )}

            {!single && (
              <div className="flex flex-wrap gap-1.5 px-4 pb-4" role="group" aria-label={`${family.label} version`}>
                {family.versions.map(({ template, version }) => {
                  const active = template.id === value
                  return (
                    <button
                      key={template.id}
                      type="button"
                      aria-pressed={active}
                      onClick={() => onChange(template.id)}
                      className={cn(
                        "min-h-8 rounded-md border px-2.5 text-xs font-medium tabular-nums transition-colors duration-150 active:scale-[0.97] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1",
                        active
                          ? "border-primary bg-primary text-primary-foreground"
                          : "bg-background text-muted-foreground hover:border-foreground/30 hover:text-foreground"
                      )}
                    >
                      {version || template.name}
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
