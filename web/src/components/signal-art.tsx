"use client"

import { useEffect, useRef } from "react"

// The picture on the sign-in screen: the product's own stacked reading traces,
// drawn large in the operator's colour. Every trace runs toward the right edge
// ("now") and gets brighter as it arrives. The shapes are worked out from the
// operator's name, so each panel has its own picture and it is the same on
// every visit. It is artwork, not data: nothing here is a measurement.

function seedFrom(text: string): number {
  let h = 2166136261
  for (const c of text) {
    h ^= c.codePointAt(0)!
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

function rng(seed: number) {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

interface Trace {
  hot: boolean
  gain: number
  waves: { f: number; p: number; a: number }[]
  bursts: { x: number; w: number; a: number; r: number }[]
}

const COUNT = 28

function makeTraces(seed: string): Trace[] {
  const rand = rng(seedFrom(seed))
  const traces: Trace[] = Array.from({ length: COUNT }, () => ({
    hot: false,
    gain: 0.55 + rand() * 0.75,
    waves: Array.from({ length: 4 }, () => ({ f: 0.5 + rand() * 3, p: rand() * Math.PI * 2, a: 0.25 + rand() * 0.75 })),
    bursts: Array.from({ length: 1 + Math.floor(rand() * 2) }, () => ({
      x: 0.3 + rand() * 0.62,
      w: 0.035 + rand() * 0.075,
      a: 0.7 + rand() * 1.5,
      r: 60 + rand() * 90,
    })),
  }))
  // Roughly every fifth trace is a bright one.
  for (let i = 1 + Math.floor(rand() * 3); i < COUNT; i += 4 + Math.floor(rand() * 3)) traces[i].hot = true
  return traces
}

function level(t: Trace, x: number): number {
  let v = 0
  for (const w of t.waves) v += Math.sin(x * w.f * Math.PI * 2 + w.p) * w.a
  let b = 0
  for (const k of t.bursts) b += k.a * Math.exp(-((x - k.x) ** 2) / (2 * k.w * k.w)) * (0.55 + 0.3 * Math.sin(x * k.r + k.x * 40) + 0.15 * Math.sin(x * k.r * 2.7 + k.w * 300))
  return (v * 0.13 + b) * t.gain
}

export function SignalArt({ seed, hue, saturation, className }: { seed: string; hue: number; saturation: number; className?: string }) {
  const ref = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = ref.current
    const ctx = canvas?.getContext("2d")
    if (!canvas || !ctx) return
    const traces = makeTraces(seed)
    // The picture is always vivid, whatever the brand colour's own strength: a
    // greyed navy still gives a blue field, only its hue is the operator's.
    const sat = Math.max(saturation, 78)
    const ink = (light: number, alpha: number) => `hsl(${hue} ${sat}% ${light}% / ${alpha})`

    const draw = (progress: number) => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2)
      const W = canvas.clientWidth
      const H = canvas.clientHeight
      if (W === 0 || H === 0) return
      if (canvas.width !== Math.round(W * dpr) || canvas.height !== Math.round(H * dpr)) {
        canvas.width = Math.round(W * dpr)
        canvas.height = Math.round(H * dpr)
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
      ctx.clearRect(0, 0, W, H)
      ctx.lineJoin = "round"
      ctx.lineCap = "round"

      const top = H * 0.08
      const gap = (H * 0.84) / (COUNT - 1)
      const swing = gap * 2.3
      const edge = W - 36
      const reach = edge * progress
      const yAt = (t: Trace, i: number, x: number) => top + i * gap - level(t, x / W) * swing

      traces.forEach((t, i) => {
        const fade = ctx.createLinearGradient(0, 0, W, 0)
        fade.addColorStop(0, ink(70, 0))
        fade.addColorStop(0.3, ink(66, t.hot ? 0.25 : 0.12))
        fade.addColorStop(1, ink(t.hot ? 82 : 66, t.hot ? 1 : 0.6))
        ctx.beginPath()
        for (let x = 0; x <= reach; x += 3) {
          const y = yAt(t, i, x)
          if (x === 0) ctx.moveTo(x, y)
          else ctx.lineTo(x, y)
        }
        ctx.strokeStyle = fade
        ctx.lineWidth = t.hot ? 1.8 : 1
        ctx.shadowColor = t.hot ? ink(58, 1) : "transparent"
        ctx.shadowBlur = t.hot ? 22 : 0
        ctx.stroke()
      })

      // Points of light: where a bright trace peaks, and where it reaches "now".
      ctx.shadowBlur = 0
      const glow = (x: number, y: number, r: number, strength: number) => {
        const halo = ctx.createRadialGradient(x, y, 0, x, y, r)
        halo.addColorStop(0, ink(92, 0.95 * strength))
        halo.addColorStop(0.18, ink(68, 0.6 * strength))
        halo.addColorStop(1, ink(60, 0))
        ctx.fillStyle = halo
        ctx.beginPath()
        ctx.arc(x, y, r, 0, Math.PI * 2)
        ctx.fill()
      }
      traces.forEach((t, i) => {
        if (!t.hot) return
        for (const k of t.bursts) {
          const x = k.x * W
          if (x <= reach) glow(x, yAt(t, i, x), 26 + k.a * 10, 0.3 + k.x * 0.6)
        }
        if (progress >= 1) glow(edge, yAt(t, i, edge), 30, 1)
      })
    }

    // One moment on load: the traces draw in from the left. Skipped when the
    // device asks for reduced motion; a resize redraws the finished picture.
    let frame = 0
    const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches
    if (still) draw(1)
    else {
      const started = performance.now()
      const step = (now: number) => {
        const p = Math.min((now - started) / 1200, 1)
        draw(1 - (1 - p) ** 3)
        if (p < 1) frame = requestAnimationFrame(step)
      }
      frame = requestAnimationFrame(step)
    }
    const ro = new ResizeObserver(() => {
      cancelAnimationFrame(frame)
      draw(1)
    })
    ro.observe(canvas)
    return () => {
      cancelAnimationFrame(frame)
      ro.disconnect()
    }
  }, [seed, hue, saturation])

  return <canvas ref={ref} aria-hidden="true" className={className} />
}
