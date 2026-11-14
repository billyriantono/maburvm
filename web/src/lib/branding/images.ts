// Browser-side image helpers for the brand studio.

const MAX_BYTES = 280 * 1024

function readAsDataURL(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(String(r.result))
    r.onerror = () => reject(new Error("Could not read the file"))
    r.readAsDataURL(file)
  })
}

function load(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error("That file is not an image this browser can read"))
    img.src = src
  })
}

const ACCEPTED = ["image/png", "image/jpeg", "image/webp", "image/svg+xml", "image/x-icon", "image/vnd.microsoft.icon"]
export const ACCEPT_ATTR = ACCEPTED.join(",")

/**
 * Turn a chosen file into a data URL small enough to store. Vector files are
 * kept as they are; photos and large bitmaps are scaled down to `maxSide`.
 */
export async function prepareImage(file: File, maxSide: number): Promise<string> {
  if (!ACCEPTED.includes(file.type)) throw new Error("Use a PNG, JPEG, WebP, SVG or ICO file")
  const raw = await readAsDataURL(file)
  if (file.type === "image/svg+xml") {
    if (raw.length > MAX_BYTES) throw new Error("That SVG is larger than 200 KB. Simplify it or export a PNG.")
    // Re-encode as base64 so the stored value has one predictable shape.
    return raw.includes(";base64,") ? raw : `data:image/svg+xml;base64,${btoa(decodeURIComponent(raw.split(",")[1]))}`
  }
  const img = await load(raw)
  const scale = Math.min(1, maxSide / Math.max(img.naturalWidth, img.naturalHeight))
  if (scale === 1 && raw.length <= MAX_BYTES && file.type !== "image/x-icon" && file.type !== "image/vnd.microsoft.icon") return raw
  const canvas = document.createElement("canvas")
  canvas.width = Math.max(1, Math.round(img.naturalWidth * scale))
  canvas.height = Math.max(1, Math.round(img.naturalHeight * scale))
  canvas.getContext("2d")!.drawImage(img, 0, 0, canvas.width, canvas.height)
  const out = canvas.toDataURL("image/png")
  if (out.length > MAX_BYTES) throw new Error("That image is still too large after resizing. Try a simpler logo.")
  return out
}

/**
 * The colours a logo is mostly made of, most common first. Greys, near-white
 * and near-black are left out: they are rarely the brand colour.
 */
export async function logoColours(dataUrl: string, limit = 5): Promise<string[]> {
  const img = await load(dataUrl)
  const size = 48
  const canvas = document.createElement("canvas")
  canvas.width = size
  canvas.height = size
  const ctx = canvas.getContext("2d")!
  ctx.drawImage(img, 0, 0, size, size)
  const { data } = ctx.getImageData(0, 0, size, size)
  const buckets = new Map<number, { n: number; r: number; g: number; b: number }>()
  for (let i = 0; i < data.length; i += 4) {
    const [r, g, b, a] = [data[i], data[i + 1], data[i + 2], data[i + 3]]
    if (a < 200) continue
    const max = Math.max(r, g, b)
    const min = Math.min(r, g, b)
    if (max - min < 40 || max < 40 || min > 225) continue
    const key = ((r >> 4) << 8) | ((g >> 4) << 4) | (b >> 4)
    const e = buckets.get(key) ?? { n: 0, r: 0, g: 0, b: 0 }
    e.n++; e.r += r; e.g += g; e.b += b
    buckets.set(key, e)
  }
  const hex = (v: number) => Math.round(v).toString(16).padStart(2, "0")
  return [...buckets.values()]
    .sort((x, y) => y.n - x.n)
    .slice(0, limit)
    .map((e) => `#${hex(e.r / e.n)}${hex(e.g / e.n)}${hex(e.b / e.n)}`)
}

const PHOTO_BYTES = 850 * 1024

/**
 * A photograph for the picture side of the sign-in page, as a JPEG data URL
 * small enough to store. Scaled to fit 1800px and compressed in steps until it
 * fits; a picture that still does not fit is refused with the reason.
 */
export async function preparePhoto(file: File): Promise<string> {
  if (!["image/png", "image/jpeg", "image/webp"].includes(file.type)) throw new Error("Use a PNG, JPEG or WebP picture")
  const img = await load(await readAsDataURL(file))
  for (const [side, quality] of [[1800, 0.84], [1800, 0.72], [1400, 0.72], [1100, 0.68]] as const) {
    const scale = Math.min(1, side / Math.max(img.naturalWidth, img.naturalHeight))
    const canvas = document.createElement("canvas")
    canvas.width = Math.max(1, Math.round(img.naturalWidth * scale))
    canvas.height = Math.max(1, Math.round(img.naturalHeight * scale))
    canvas.getContext("2d")!.drawImage(img, 0, 0, canvas.width, canvas.height)
    const out = canvas.toDataURL("image/jpeg", quality)
    if (out.length <= PHOTO_BYTES) return out
  }
  throw new Error("That picture is too detailed to store even after shrinking. Try a simpler one.")
}
