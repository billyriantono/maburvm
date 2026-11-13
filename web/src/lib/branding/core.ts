// Branding shared by the server layout and client components: the data shape,
// colour maths and the generated monogram. No React and no browser APIs here.

export interface Branding {
  name: string
  tagline: string
  logo: string
  favicon: string
  accent: string
  login_headline: string
  login_message: string
  support_url: string
  support_email: string
  footer_text: string
}

export const DEFAULT_BRANDING: Branding = {
  name: "MaburVM",
  tagline: "",
  logo: "",
  favicon: "",
  accent: "",
  login_headline: "",
  login_message: "",
  support_url: "",
  support_email: "",
  footer_text: "",
}

/** The built-in colour used when the operator has not picked one. Matches
 *  --primary in globals.css. */
export const NEUTRAL_ACCENT = "#1f4fd8"

export function accentOf(b: Pick<Branding, "accent">): string {
  return /^#[0-9a-f]{6}$/i.test(b.accent) ? b.accent.toLowerCase() : NEUTRAL_ACCENT
}

function rgb(hex: string): [number, number, number] {
  const n = parseInt(hex.slice(1), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

/** WCAG 2.x relative luminance. */
export function luminance(hex: string): number {
  const [r, g, b] = rgb(hex).map((v) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

/** WCAG contrast ratio between two colours, unrounded. */
export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

const INK = "#0a0a0a"
const PAPER = "#fafafa"

/** Text colour that reads best on top of the accent. */
export function onAccent(accent: string): string {
  return contrast(accent, PAPER) >= contrast(accent, INK) ? PAPER : INK
}

/** "h s% l%", the form the design tokens use. */
export function hsl(hex: string): string {
  const [r, g, b] = rgb(hex).map((v) => v / 255)
  const max = Math.max(r, g, b)
  const min = Math.min(r, g, b)
  const l = (max + min) / 2
  const d = max - min
  let h = 0
  let s = 0
  if (d !== 0) {
    s = d / (1 - Math.abs(2 * l - 1))
    if (max === r) h = ((g - b) / d) % 6
    else if (max === g) h = (b - r) / d + 2
    else h = (r - g) / d + 4
    h *= 60
    if (h < 0) h += 360
  }
  return `${Math.round(h)} ${Math.round(s * 100)}% ${Math.round(l * 100)}%`
}

/** CSS variables that recolour the whole interface. Empty when the operator
 *  has not picked a colour, so the stylesheet's own light and dark values apply. */
export function themeVars(b: Pick<Branding, "accent">): Record<string, string> {
  if (!/^#[0-9a-f]{6}$/i.test(b.accent)) return {}
  const accent = accentOf(b)
  return {
    "--primary": hsl(accent),
    "--primary-foreground": hsl(onAccent(accent)),
    "--ring": hsl(accent),
  }
}

/** Up to two letters standing in for a logo: "Acme Cloud" gives "AC". */
export function initials(name: string): string {
  const words = name.trim().split(/[\s\-_.]+/).filter(Boolean)
  if (words.length === 0) return "?"
  if (words.length === 1) return Array.from(words[0]).slice(0, 2).join("").toUpperCase()
  return (Array.from(words[0])[0] + Array.from(words[1])[0]).toUpperCase()
}

/** A square mark drawn from the name and colour, as an SVG data URL. */
export function monogram(b: Pick<Branding, "name" | "accent">): string {
  const accent = accentOf(b)
  const text = initials(b.name).replace(/[<>&"']/g, "")
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` +
    `<rect width="64" height="64" rx="14" fill="${accent}"/>` +
    `<text x="32" y="33" text-anchor="middle" dominant-baseline="central" ` +
    `font-family="ui-sans-serif,system-ui,sans-serif" font-size="${text.length > 1 ? 26 : 32}" font-weight="700" ` +
    `fill="${onAccent(accent)}">${text}</text></svg>`
  return `data:image/svg+xml,${encodeURIComponent(svg)}`
}

export function markOf(b: Branding): string {
  return b.logo || monogram(b)
}

export function faviconOf(b: Branding): string {
  return b.favicon || monogram(b)
}

/** Fill gaps in whatever the server sent, so callers never see undefined. */
export function withDefaults(b: Partial<Branding> | null | undefined): Branding {
  const out = { ...DEFAULT_BRANDING, ...(b ?? {}) }
  if (!out.name) out.name = DEFAULT_BRANDING.name
  return out
}
