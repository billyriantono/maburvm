import { NextRequest } from "next/server"

export const dynamic = "force-dynamic"

export async function GET(request: NextRequest) {
  const encoder = new TextEncoder()
  
  const stream = new ReadableStream({
    start(controller) {
      // Send initial connection message
      controller.enqueue(
        encoder.encode(`data: ${JSON.stringify({ type: "connected", timestamp: new Date().toISOString() })}\n\n`)
      )
      
      // Simulate periodic VM status updates
      const interval = setInterval(() => {
        // Randomly simulate VM status changes for demo
        const mockStatuses = ["running", "stopped", "suspended"]
        const randomVmId = Math.floor(Math.random() * 10) + 1
        const randomStatus = mockStatuses[Math.floor(Math.random() * mockStatuses.length)]
        
        const update = {
          type: "vm_update",
          vm: {
            id: String(randomVmId),
            status: randomStatus,
            timestamp: new Date().toISOString()
          }
        }
        
        try {
          controller.enqueue(encoder.encode(`data: ${JSON.stringify(update)}\n\n`))
        } catch (error) {
          // Stream closed
          clearInterval(interval)
        }
      }, 10000) // Send update every 10 seconds
      
      // Cleanup on close
      request.signal.addEventListener("abort", () => {
        clearInterval(interval)
        try {
          controller.close()
        } catch (e) {
          // Already closed
        }
      })
    }
  })
  
  return new Response(stream, {
    headers: {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      "Connection": "keep-alive",
    },
  })
}