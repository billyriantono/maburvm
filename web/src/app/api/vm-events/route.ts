import { NextRequest } from "next/server"

export const dynamic = "force-dynamic"

// Server-Sent Events proxy for live VM status. The browser opens an EventSource
// to this same-origin route (so the auth cookie is sent automatically); we
// forward that token to the panel API's SSE endpoint as a Bearer header. If the
// backend stream is unavailable we fall back to authenticated polling.
export async function GET(request: NextRequest) {
  const encoder = new TextEncoder()
  // Server-only: use the non-public, build/runtime API_BASE_URL (never the
  // NEXT_PUBLIC_* client override). Local dev fallback if unset.
  const apiUrl = process.env.API_BASE_URL || "http://localhost:8080"
  const token = request.cookies.get("accessToken")?.value
  const authHeaders: Record<string, string> = token ? { Authorization: `Bearer ${token}` } : {}

  const stream = new ReadableStream({
    async start(controller) {
      let closed = false
      const safeClose = () => {
        if (closed) return
        closed = true
        try {
          controller.close()
        } catch {
          // already closed
        }
      }
      const safeEnqueue = (chunk: Uint8Array) => {
        if (closed) return
        try {
          controller.enqueue(chunk)
        } catch {
          closed = true
        }
      }

      safeEnqueue(encoder.encode(`data: ${JSON.stringify({ type: "connected", timestamp: new Date().toISOString() })}\n\n`))

      try {
        // Prefer the backend SSE stream; aborts when the browser disconnects.
        const response = await fetch(`${apiUrl}/api/v1/events/vm-status`, {
          headers: { Accept: "text/event-stream", ...authHeaders },
          signal: request.signal,
        })
        if (!response.ok || !response.body) {
          throw new Error("SSE endpoint not available")
        }

        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          safeEnqueue(encoder.encode(decoder.decode(value, { stream: true })))
        }
        safeClose()
      } catch {
        // Client disconnected — don't start polling, just close.
        if (request.signal.aborted) {
          safeClose()
          return
        }

        // Fallback: authenticated polling of the VM list.
        const poll = async () => {
          try {
            const vmsResponse = await fetch(`${apiUrl}/api/v1/vms`, { headers: authHeaders })
            if (vmsResponse.ok) {
              const vms = await vmsResponse.json()
              safeEnqueue(
                encoder.encode(`data: ${JSON.stringify({ type: "vm_list", data: vms, timestamp: new Date().toISOString() })}\n\n`)
              )
            }
          } catch {
            // API not available, skip this poll
          }
        }
        await poll()
        const pollInterval = setInterval(poll, 10000)
        request.signal.addEventListener("abort", () => {
          clearInterval(pollInterval)
          safeClose()
        })
      }
    },
  })

  return new Response(stream, {
    headers: {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
    },
  })
}
