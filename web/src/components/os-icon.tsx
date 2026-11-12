import type { IconType } from "react-icons"
import {
  SiUbuntu,
  SiDebian,
  SiCentos,
  SiFedora,
  SiArchlinux,
  SiAlpinelinux,
  SiRedhat,
  SiFreebsd,
  SiRockylinux,
  SiAlmalinux,
  SiOpensuse,
  SiKalilinux,
  SiLinux,
  SiMikrotik,
} from "react-icons/si"

// The icon set carries no Windows mark, so this is the plain four-pane glyph.
const WindowsGlyph: IconType = (props) => (
  <svg viewBox="0 0 24 24" fill="currentColor" {...props}>
    <path d="M3 5.5 10.5 4.4v7.1H3zM11.5 4.3 21 3v8.5h-9.5zM3 12.5h7.5v7.1L3 18.5zM11.5 12.5H21V21l-9.5-1.3z" />
  </svg>
)

interface OSDef {
  label: string // the distribution's own name, used to group its versions
  match: string[] // lowercase substrings to look for in the template name
  Icon: IconType
  color: string // official brand color
}

// Order matters: more specific names first so e.g. "rocky"/"alma" win before a
// generic match. Each entry uses the distro's official brand colour.
const OS_DEFS: OSDef[] = [
  { label: "Ubuntu", match: ["ubuntu"], Icon: SiUbuntu, color: "#E95420" },
  { label: "Debian", match: ["debian"], Icon: SiDebian, color: "#A81D33" },
  { label: "Rocky Linux", match: ["rocky"], Icon: SiRockylinux, color: "#10B981" },
  { label: "AlmaLinux", match: ["alma"], Icon: SiAlmalinux, color: "#0D597F" },
  { label: "CentOS", match: ["centos"], Icon: SiCentos, color: "#262577" },
  { label: "Fedora", match: ["fedora"], Icon: SiFedora, color: "#51A2DA" },
  { label: "Arch Linux", match: ["arch"], Icon: SiArchlinux, color: "#1793D1" },
  { label: "Alpine Linux", match: ["alpine"], Icon: SiAlpinelinux, color: "#0D597F" },
  { label: "Kali Linux", match: ["kali"], Icon: SiKalilinux, color: "#557C94" },
  { label: "openSUSE", match: ["opensuse", "suse"], Icon: SiOpensuse, color: "#73BA25" },
  { label: "Red Hat", match: ["redhat", "rhel", "red hat"], Icon: SiRedhat, color: "#EE0000" },
  { label: "FreeBSD", match: ["freebsd", "bsd"], Icon: SiFreebsd, color: "#AB2B28" },
  { label: "MikroTik", match: ["mikrotik", "routeros", "chr"], Icon: SiMikrotik, color: "#293239" },
  { label: "Windows Server", match: ["windows", "win2", "win-"], Icon: WindowsGlyph, color: "#0A64C8" },
]

// osIdentity names the distribution a template belongs to, so its versions can
// be shown together under one logo. Unrecognised templates are their own group.
export function osIdentity(name?: string): { key: string; label: string; color: string; Icon: IconType } {
  const lower = (name || "").toLowerCase()
  const def = OS_DEFS.find((d) => d.match.some((m) => lower.includes(m)))
  if (def) return { key: def.label, label: def.label, color: def.color, Icon: def.Icon }
  // Drop a trailing version so "Something 2.1" and "Something 3" still group.
  const label = (name || "Other").replace(/[\s-]*v?\d[\d.]*\s*$/i, "").trim() || name || "Other"
  return { key: label.toLowerCase(), label, color: "#4B5563", Icon: SiLinux }
}

// OSLogoTile is the logo on a tile of its brand colour: recognisable at a
// glance in a grid, where a bare coloured glyph on white is not.
export function OSLogoTile({ name, className = "h-10 w-10" }: { name?: string; className?: string }) {
  const { color, Icon } = osIdentity(name)
  return (
    <span
      className={`inline-flex shrink-0 items-center justify-center rounded-lg text-white ${className}`}
      style={{ backgroundColor: color }}
      aria-hidden="true"
    >
      <Icon className="h-[55%] w-[55%]" />
    </span>
  )
}

interface OSIconProps {
  name?: string
  className?: string
}

// OSIcon renders the real distro logo (in its brand colour) for a template/OS
// name, falling back to the generic Linux (Tux) mark for anything unrecognised.
export function OSIcon({ name, className = "w-6 h-6" }: OSIconProps) {
  const lower = (name || "").toLowerCase()
  const def = OS_DEFS.find((d) => d.match.some((m) => lower.includes(m)))
  const Icon = def?.Icon ?? SiLinux
  const color = def?.color ?? "#4B5563" // neutral grey for unknown/imported
  return <Icon className={className} style={{ color }} aria-hidden />
}
