"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { toast } from "sonner"

// useFlash turns true for a moment after trigger(key). Use it to show a check
// on a Save or Copy button, then let it settle back.
export function useFlash(ms = 1500) {
  const [key, setKey] = useState<string | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const trigger = useCallback(
    (k = "default") => {
      if (timer.current) clearTimeout(timer.current)
      setKey(k)
      timer.current = setTimeout(() => setKey(null), ms)
    },
    [ms]
  )

  useEffect(() => () => { if (timer.current) clearTimeout(timer.current) }, [])

  return { active: (k = "default") => key === k, trigger }
}

// useCopy copies text and reports honestly: `copied(key)` only turns true when
// the clipboard write worked. Falls back to execCommand on plain-HTTP origins,
// where navigator.clipboard does not exist.
export function useCopy(ms = 2000) {
  const { active, trigger } = useFlash(ms)

  const copy = useCallback(
    async (text: string, key = "default") => {
      let ok = false
      try {
        await navigator.clipboard.writeText(text)
        ok = true
      } catch {
        const ta = document.createElement("textarea")
        ta.value = text
        ta.style.position = "fixed"
        ta.style.opacity = "0"
        document.body.appendChild(ta)
        ta.select()
        try { ok = document.execCommand("copy") } catch { ok = false }
        document.body.removeChild(ta)
      }
      if (ok) trigger(key)
      else toast.error("Could not copy. Select the text and copy it manually.")
      return ok
    },
    [trigger]
  )

  return { copy, copied: active }
}
