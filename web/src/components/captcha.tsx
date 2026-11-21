"use client"

import { useEffect, useRef, useState } from "react"
import { Button } from "@/components/ui/button"

/** What the panel answers when the sign-in check is needed. */
export interface CaptchaChallenge {
  provider: "turnstile" | "hcaptcha"
  site_key: string
}

/** The challenge in a sign-in or reset answer, if the panel asked for one. */
export function captchaChallenge(body: { code?: string; provider?: string; site_key?: string }): CaptchaChallenge | null {
  if ((body.code === "captcha_required" || body.code === "captcha_failed") &&
      (body.provider === "turnstile" || body.provider === "hcaptcha") && body.site_key) {
    return { provider: body.provider, site_key: body.site_key }
  }
  return null
}

interface Widgets {
  render(el: HTMLElement, opts: { sitekey: string; callback: (token: string) => void; "expired-callback": () => void }): unknown
}

declare global {
  interface Window {
    turnstile?: Widgets
    hcaptcha?: Widgets
  }
}

const SCRIPTS = {
  turnstile: "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit",
  hcaptcha: "https://js.hcaptcha.com/1/api.js?render=explicit",
}

/**
 * The provider's check. A token is good for one attempt: give the component a
 * new key after each submit to get a fresh check.
 */
export function Captcha({ challenge, onToken }: { challenge: CaptchaChallenge; onToken: (token: string) => void }) {
  const box = useRef<HTMLDivElement>(null)
  const latest = useRef(onToken)
  latest.current = onToken
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    const api = () => (challenge.provider === "turnstile" ? window.turnstile : window.hcaptcha)
    const mount = () => {
      const widgets = api()
      if (!widgets || !box.current) return setFailed(true)
      box.current.replaceChildren()
      widgets.render(box.current, {
        sitekey: challenge.site_key,
        callback: (token) => latest.current(token),
        "expired-callback": () => latest.current(""),
      })
    }
    if (api()) return mount()
    const src = SCRIPTS[challenge.provider]
    let script = document.querySelector<HTMLScriptElement>(`script[src="${src}"]`)
    if (!script) {
      script = document.createElement("script")
      script.src = src
      script.async = true
      document.head.appendChild(script)
    }
    const fail = () => setFailed(true)
    script.addEventListener("load", mount)
    script.addEventListener("error", fail)
    return () => {
      script?.removeEventListener("load", mount)
      script?.removeEventListener("error", fail)
    }
  }, [challenge.provider, challenge.site_key])

  if (failed) {
    return (
      <div role="alert" className="rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm">
        <p>The check could not be shown. Reload the page; if it still does not appear, a blocker may be stopping it.</p>
        <Button type="button" variant="outline" size="sm" className="mt-2" onClick={() => window.location.reload()}>
          Reload
        </Button>
      </div>
    )
  }
  return <div ref={box} className="min-h-[65px]" />
}
