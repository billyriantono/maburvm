"use client"

import { Suspense, useState } from "react"
import { useSearchParams } from "next/navigation"
import { Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"

// Where the link in a marketing email lands. It asks before it acts, so a mail
// scanner opening every link does not unsubscribe anyone.
function Unsubscribe() {
  const token = useSearchParams().get("token") ?? ""
  const [state, setState] = useState<"ask" | "busy" | "done" | "failed">("ask")

  const confirm = async () => {
    setState("busy")
    try {
      const res = await fetch("/api/v1/unsubscribe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      })
      setState(res.ok ? "done" : "failed")
    } catch {
      setState("failed")
    }
  }

  return (
    <div className="w-full space-y-4">
      <h1 className="text-[2rem] font-semibold leading-tight tracking-[-0.01em] text-balance text-foreground">
        {state === "done" ? "You are unsubscribed" : "Stop news and offers by email"}
      </h1>
      {state === "done" ? (
        <p className="text-sm text-muted-foreground">
          No more marketing email will be sent to you. Notices about your servers, such as planned maintenance, still reach you.
          You can turn news back on in your account settings.
        </p>
      ) : !token || state === "failed" ? (
        <p className="text-sm text-muted-foreground" role="alert">
          This link is not valid. Open the newest email again, or turn news off in your account settings.
        </p>
      ) : (
        <>
          <p className="text-sm text-muted-foreground">
            You will stop getting news and offers. Notices about your servers, such as planned maintenance, still reach you.
          </p>
          <Button onClick={confirm} disabled={state === "busy"}>
            {state === "busy" && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
            Unsubscribe
          </Button>
        </>
      )}
    </div>
  )
}

export default function UnsubscribePage() {
  return (
    <Suspense>
      <Unsubscribe />
    </Suspense>
  )
}
