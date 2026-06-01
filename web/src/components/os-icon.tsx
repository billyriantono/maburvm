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
} from "react-icons/si"

interface OSDef {
  match: string[] // lowercase substrings to look for in the template name
  Icon: IconType
  color: string // official brand color
}

// Order matters: more specific names first so e.g. "rocky"/"alma" win before a
// generic match. Each entry uses the distro's official brand colour.
const OS_DEFS: OSDef[] = [
  { match: ["ubuntu"], Icon: SiUbuntu, color: "#E95420" },
  { match: ["debian"], Icon: SiDebian, color: "#A81D33" },
  { match: ["rocky"], Icon: SiRockylinux, color: "#10B981" },
  { match: ["alma"], Icon: SiAlmalinux, color: "#0D597F" },
  { match: ["centos"], Icon: SiCentos, color: "#262577" },
  { match: ["fedora"], Icon: SiFedora, color: "#51A2DA" },
  { match: ["arch"], Icon: SiArchlinux, color: "#1793D1" },
  { match: ["alpine"], Icon: SiAlpinelinux, color: "#0D597F" },
  { match: ["kali"], Icon: SiKalilinux, color: "#557C94" },
  { match: ["opensuse", "suse"], Icon: SiOpensuse, color: "#73BA25" },
  { match: ["redhat", "rhel", "red hat"], Icon: SiRedhat, color: "#EE0000" },
  { match: ["freebsd", "bsd"], Icon: SiFreebsd, color: "#AB2B28" },
]

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
