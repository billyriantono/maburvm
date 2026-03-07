import { NextRequest } from "next/server"

export const dynamic = "force-dynamic"

export async function GET(request: NextRequest) {
  const encoder = new TextEncoder()
  const apiUrl = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080"
  
  const stream = new ReadableStream({
    async start(controller) {
      controller.enqueue(
        encoder.encode(`data: ${JSON.stringify({ type: "connected", timestamp: new Date().toISOString() })}\n\n`)
      )
      
      try {
        const response = await fetch(`${apiUrl}/api/v1/events/vm-status`, {
          headers: {
            Accept: "text/event-stream",
          },
        })
        
        if (!response.ok || !response.body) {
          throw new Error("SSE endpoint not available, using polling fallback")
        }
        
        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        
        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          
          const chunk = decoder.decode(value, { stream: true })
          controller.enqueue(encoder.encode(chunk))
        }
      } catch {
        const pollInterval = setInterval(async () => {
          try {
            const vmsResponse = await fetch(`${apiUrl}/api/v1/vms`)
            if (vmsResponse.ok) {
              const vms = await vmsResponse.json()
              controller.enqueue(
                encoder.encode(`data: ${JSON.stringify({ type: "vm_list", vms, timestamp: new Date().toISOString() })}\n\n`)
              )
            }
          } catch {
            // API not available, skip this poll
          }
        }, 10000)
        
        request.signal.addEventListener("abort", () => {
          clearInterval(pollInterval)
          try {
            controller.close()
          } catch {
            // Already closed
          }
        })
      }
    },
  })
  
  return new Response(stream, {
    headers: {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      "Connection": "keep-alive",
    },
  })
}