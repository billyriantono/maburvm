"use client"

import { useState } from "react"
import Link from "next/link"
import { Loader2, AlertTriangle, MailCheck } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState("")
  const [isLoading, setIsLoading] = useState(false)
  const [sent, setSent] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setIsLoading(true)
    setError(null)
    try {
      // Same-origin: /api is proxied server-side to the panel.
      const response = await fetch(`/api/v1/auth/forgot-password`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email }),
      })
      if (!response.ok) {
        const result = await response.json().catch(() => ({}))
        setError(result.error || "Something went wrong. Please try again.")
        return
      }
      // Always a generic success — the backend never reveals whether the email exists.
      setSent(true)
    } catch {
      setError("An unexpected error occurred. Please try again.")
    } finally {
      setIsLoading(false)
    }
  }

  if (sent) {
    return (
      <div className="w-full text-center space-y-4">
        <MailCheck className="w-10 h-10 text-primary mx-auto" />
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">Check your email</h2>
        <p className="text-sm text-muted-foreground">
          If an account exists for <span className="font-medium text-foreground">{email}</span>, we&apos;ve
          sent a link to reset your password. The link is valid for 1 hour.
        </p>
        <Link href="/login" className="inline-block text-sm font-medium text-primary hover:underline">
          Back to sign in
        </Link>
      </div>
    )
  }

  return (
    <div className="w-full">
      <h2 className="text-2xl font-semibold tracking-tight mb-2 text-foreground">Reset your password</h2>
      <p className="text-sm text-muted-foreground mb-6">
        Enter your email and we&apos;ll send you a link to reset your password.
      </p>

      {error && (
        <div className="mb-6 p-4 bg-destructive/10 border border-destructive/20 rounded-lg">
          <div className="flex items-start gap-3">
            <AlertTriangle className="w-5 h-5 text-destructive shrink-0 mt-0.5" />
            <p className="font-medium text-destructive text-sm">{error}</p>
          </div>
        </div>
      )}

      <form onSubmit={onSubmit} className="space-y-5">
        <div>
          <label htmlFor="email" className="block text-sm font-medium text-foreground mb-2">
            Email Address
          </label>
          <Input
            id="email"
            type="email"
            required
            placeholder="you@company.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            disabled={isLoading}
          />
        </div>

        <Button type="submit" size="xl" className="w-full text-base font-medium" disabled={isLoading || !email}>
          {isLoading ? (
            <>
              <Loader2 className="w-5 h-5 animate-spin mr-2" />
              <span>Sending...</span>
            </>
          ) : (
            "Send reset link"
          )}
        </Button>
      </form>

      <div className="mt-6 text-center">
        <Link href="/login" className="text-sm font-medium text-primary hover:underline">
          Back to sign in
        </Link>
      </div>
    </div>
  )
}
