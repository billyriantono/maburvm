"use client"

import { Suspense, useState } from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { Eye, EyeOff, Loader2, AlertTriangle, CheckCircle2 } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"

function ResetPasswordForm() {
  const router = useRouter()
  const searchParams = useSearchParams()
  const token = searchParams.get("token") ?? ""

  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [showPassword, setShowPassword] = useState(false)
  const [isLoading, setIsLoading] = useState(false)
  const [done, setDone] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    if (password !== confirm) {
      setError("Passwords do not match.")
      return
    }
    setIsLoading(true)
    try {
      const response = await fetch(`/api/v1/auth/reset-password`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, password }),
      })
      if (!response.ok) {
        const result = await response.json().catch(() => ({}))
        setError(result.error || "Failed to reset password.")
        return
      }
      setDone(true)
      setTimeout(() => router.push("/login"), 2000)
    } catch {
      setError("An unexpected error occurred. Please try again.")
    } finally {
      setIsLoading(false)
    }
  }

  if (!token) {
    return (
      <div className="w-full text-center space-y-4">
        <AlertTriangle className="w-10 h-10 text-destructive mx-auto" />
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">Invalid reset link</h2>
        <p className="text-sm text-muted-foreground">
          This link is missing its reset token. Please request a new password reset.
        </p>
        <Link href="/forgot-password" className="inline-block text-sm font-medium text-primary hover:underline">
          Request a new link
        </Link>
      </div>
    )
  }

  if (done) {
    return (
      <div className="w-full text-center space-y-4">
        <CheckCircle2 className="w-10 h-10 text-primary mx-auto" />
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">Password reset</h2>
        <p className="text-sm text-muted-foreground">Redirecting you to sign in…</p>
      </div>
    )
  }

  return (
    <div className="w-full">
      <h2 className="text-2xl font-semibold tracking-tight mb-2 text-foreground">Set a new password</h2>
      <p className="text-sm text-muted-foreground mb-6">
        Choose a strong password — at least 12 characters with upper, lower, a digit and a symbol.
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
          <label htmlFor="password" className="block text-sm font-medium text-foreground mb-2">
            New Password
          </label>
          <div className="relative">
            <Input
              id="password"
              type={showPassword ? "text" : "password"}
              required
              placeholder="Enter a new password"
              className="pr-12"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={isLoading}
            />
            <button
              type="button"
              onClick={() => setShowPassword(!showPassword)}
              className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
              tabIndex={-1}
            >
              {showPassword ? <EyeOff className="w-5 h-5" /> : <Eye className="w-5 h-5" />}
            </button>
          </div>
        </div>

        <div>
          <label htmlFor="confirm" className="block text-sm font-medium text-foreground mb-2">
            Confirm Password
          </label>
          <Input
            id="confirm"
            type={showPassword ? "text" : "password"}
            required
            placeholder="Re-enter the password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            disabled={isLoading}
          />
        </div>

        <Button
          type="submit"
          size="xl"
          className="w-full text-base font-medium"
          disabled={isLoading || !password || !confirm}
        >
          {isLoading ? (
            <>
              <Loader2 className="w-5 h-5 animate-spin mr-2" />
              <span>Resetting...</span>
            </>
          ) : (
            "Reset password"
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

export default function ResetPasswordPage() {
  return (
    <Suspense fallback={<div className="flex justify-center py-8"><Loader2 className="w-6 h-6 animate-spin text-muted-foreground" /></div>}>
      <ResetPasswordForm />
    </Suspense>
  )
}
