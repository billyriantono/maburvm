import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

const BIT_UNITS = ["b/s", "Kb/s", "Mb/s", "Gb/s", "Tb/s"]
const BYTE_UNITS = ["B/s", "KB/s", "MB/s", "GB/s"]
const NICE = [1, 2, 5, 10, 20, 50, 100, 200, 500, 1000]

/** Picks one unit for a pair of byte rates and a round top for the axis.
 * With bits (network), the rates read in b/s, Kb/s, Mb/s, 1000-based; the
 * values, top and fmt input stay in bytes per second. */
export function rateScale(values: number[][], live: (number | undefined)[] = [], bits = false) {
  const [units, base, mul] = bits ? [BIT_UNITS, 1000, 8] : [BYTE_UNITS, 1024, 1]
  const peak = Math.max(1, ...values.flat(), ...live.map((v) => v ?? 0)) * mul
  let unit = 0
  let scaled = peak
  while (scaled >= base && unit < units.length - 1) {
    scaled /= base
    unit++
  }
  const div = base ** unit / mul // bytes per second in one displayed unit
  const top = NICE.find((n) => n >= scaled) ?? Math.ceil(scaled)
  const fmt = (v: number) => {
    const x = v / div
    return `${x >= 100 ? x.toFixed(0) : x.toFixed(1)} ${units[unit]}`
  }
  return { top: top * div, axis: `${top} ${units[unit]}`, fmt }
}

/** A network rate, given in bytes per second, shown in bits per second
 * (1000-based, the way links and ports are sold). Disk rates stay in bytes. */
export function bitRate(bytesPerSec: number): string {
  let v = Number.isFinite(bytesPerSec) ? Math.max(bytesPerSec, 0) * 8 : 0
  let u = 0
  while (v >= 1000 && u < BIT_UNITS.length - 1) {
    v /= 1000
    u++
  }
  return `${v >= 10 || u === 0 ? Math.round(v) : v.toFixed(1)} ${BIT_UNITS[u]}`
}
