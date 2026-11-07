"use client"

import { useEffect, useState } from "react"
import { useForm } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { z } from "zod"
import { Eye, EyeOff, Loader2, AlertTriangle, ShieldCheck } from "lucide-react"
import { useRouter } from "next/navigation"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"

const loginSchema = z.object({
  email: z.string().email("Please enter a valid email address"),
  password: z.string().min(1, "Password is required"),
  totpCode: z.string().optional(),
})

type LoginFormData = z.infer<typeof loginSchema>

// Messages for /login?sso=… after an SSO round trip (see handler/oidc.go).
const SSO_MESSAGES: Record<string, string> = {
  noaccount: "You signed in with SSO, but no panel account uses that email. Ask an administrator to add you.",
  ip: "Your IP address is not whitelisted for this account.",
  expired: "The SSO sign-in expired or was opened in another browser. Please try again.",
  error: "SSO sign-in failed. Please try again or sign in with your password.",
}

function storeSession(token: string) {
  document.cookie = `accessToken=${token}; path=/; max-age=${24 * 60 * 60}; SameSite=Lax`
}

interface LoginError {
  message: string
  code?: string
  requires2FA?: boolean
  userIP?: string
}

export default function LoginPage() {
  const router = useRouter()
  const [showPassword, setShowPassword] = useState(false)
  const [isLoading, setIsLoading] = useState(false)
  const [requires2FA, setRequires2FA] = useState(false)
  const [error, setError] = useState<LoginError | null>(null)
  const [ipWarning, setIpWarning] = useState<LoginError | null>(null)
  const [ssoLabel, setSsoLabel] = useState<string | null>(null)
  const [ssoTicket, setSsoTicket] = useState<string | null>(null)
  const [ssoCode, setSsoCode] = useState("")

  useEffect(() => {
    fetch("/api/v1/auth/oidc/config")
      .then((r) => r.json())
      .then((c) => setSsoLabel(c.enabled ? c.label : null))
      .catch(() => setSsoLabel(null))

    const sso = new URLSearchParams(window.location.search).get("sso")
    if (!sso) return
    if (sso === "2fa") {
      // The ticket rides in the fragment so it never reaches a server log.
      const ticket = new URLSearchParams(window.location.hash.slice(1)).get("ticket")
      if (ticket) setSsoTicket(ticket)
    } else if (sso === "ip") {
      setIpWarning({ message: SSO_MESSAGES.ip })
    } else {
      setError({ message: SSO_MESSAGES[sso] || SSO_MESSAGES.error })
    }
    window.history.replaceState(null, "", window.location.pathname)
  }, [])

  const handleSso2FA = async (ev: React.FormEvent) => {
    ev.preventDefault()
    setIsLoading(true)
    setError(null)
    try {
      const response = await fetch("/api/v1/auth/oidc/2fa", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ticket: ssoTicket, totpCode: ssoCode }),
      })
      const result = await response.json()
      if (!response.ok) {
        setError({ message: result.error || "Invalid verification code" })
        return
      }
      storeSession(result.token)
      router.push("/dashboard")
    } catch {
      setError({ message: "An unexpected error occurred. Please try again." })
    } finally {
      setIsLoading(false)
    }
  }

  const {
    register,
    handleSubmit,
    formState: { errors },
    trigger,
  } = useForm<LoginFormData>({
    resolver: zodResolver(loginSchema),
    defaultValues: {
      email: "",
      password: "",
      totpCode: "",
    },
  })

  const onSubmit = async (data: LoginFormData) => {
    setIsLoading(true)
    setError(null)
    setIpWarning(null)

    try {
      // Same-origin: /api is proxied server-side to the panel via next.config.js
      // rewrites(), so the panel hostname never reaches the client bundle.
      const response = await fetch(`/api/v1/auth/login`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(data),
      })

      const result = await response.json()

      if (!response.ok) {
        // The panel answers 403 only when the IP isn't whitelisted.
        if (response.status === 403) {
          setIpWarning({ message: result.error || "Your IP address is not whitelisted" })
          return
        }
        setError({ message: result.error || "Login failed. Please try again." })
        return
      }

      // 2FA users get a 200 without a token; ask for the code next.
      if (result.requires2fa) {
        setRequires2FA(true)
        return
      }

      // Success - store token and redirect to dashboard
      storeSession(result.token)
      router.push("/dashboard")
    } catch (err) {
      setError({
        message: "An unexpected error occurred. Please try again.",
      })
    } finally {
      setIsLoading(false)
    }
  }

  const handle2FA = async (data: LoginFormData) => {
    setIsLoading(true)
    setError(null)

    try {
      // Same-origin: /api is proxied server-side to the panel via next.config.js
      // rewrites(), so the panel hostname never reaches the client bundle.
      const response = await fetch(`/api/v1/auth/login`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          email: data.email,
          password: data.password,
          totpCode: data.totpCode,
        }),
      })

      const result = await response.json()

      if (!response.ok) {
        setError({ message: result.error || "Invalid verification code" })
        return
      }

      // Success - store token and redirect to dashboard
      storeSession(result.token)
      router.push("/dashboard")
    } catch (err) {
      setError({
        message: "An unexpected error occurred. Please try again.",
      })
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <div className="w-full">
      <h2 className="text-2xl font-semibold tracking-tight mb-6 text-foreground">
        Sign In
      </h2>

      {/* IP Whitelist Warning */}
      {ipWarning && (
        <div className="mb-6 p-4 bg-amber-50 border border-amber-200 rounded-lg dark:bg-amber-950 dark:border-amber-900">
          <div className="flex items-start gap-3">
            <AlertTriangle className="w-5 h-5 text-amber-600 shrink-0 mt-0.5" />
            <div>
              <p className="font-semibold text-amber-800 text-sm dark:text-amber-300">
                Access Denied
              </p>
              <p className="text-amber-700 text-sm mt-1">
                {ipWarning.message}
              </p>
              {ipWarning.userIP && (
                <p className="text-amber-600 text-xs mt-2 font-mono">
                  Your IP: {ipWarning.userIP}
                </p>
              )}
              <p className="text-amber-700 text-xs mt-2">
                Contact your administrator to whitelist your IP address or try from an allowed device.
              </p>
            </div>
          </div>
        </div>
      )}

      {/* General Error */}
      {error && !ipWarning && (
        <div className="mb-6 p-4 bg-destructive/10 border border-destructive/20 rounded-lg">
          <div className="flex items-start gap-3">
            <AlertTriangle className="w-5 h-5 text-destructive shrink-0 mt-0.5" />
            <p className="font-medium text-destructive text-sm">{error.message}</p>
          </div>
        </div>
      )}

      {/* SSO sign-in that stopped at the panel's own two-factor step */}
      {ssoTicket ? (
        <form onSubmit={handleSso2FA} className="space-y-5">
          <div>
            <div className="flex items-center gap-2 mb-2">
              <ShieldCheck className="w-4 h-4 text-muted-foreground" />
              <label htmlFor="ssoCode" className="block text-sm font-medium text-foreground">
                Two-Factor Code
              </label>
            </div>
            <Input
              id="ssoCode"
              type="text"
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={8}
              placeholder="123456"
              className="tracking-widest text-center"
              value={ssoCode}
              onChange={(e) => setSsoCode(e.target.value.trim())}
              disabled={isLoading}
              autoFocus
            />
            <p className="mt-2 text-xs text-muted-foreground">
              You signed in with SSO. Enter the 6-digit code from your authenticator app, or a backup code.
            </p>
          </div>
          <Button type="submit" size="xl" className="w-full text-base font-medium" disabled={isLoading || !ssoCode}>
            {isLoading ? <Loader2 className="w-5 h-5 animate-spin" /> : "Verify"}
          </Button>
        </form>
      ) : (
      <>
      {ssoLabel && !requires2FA && (
        <div className="mb-6 space-y-6">
          <Button asChild variant="outline" size="xl" className="w-full text-base font-medium">
            <a href="/api/v1/auth/oidc/login">{ssoLabel}</a>
          </Button>
          <div className="flex items-center gap-3 text-xs text-muted-foreground">
            <span className="h-px flex-1 bg-border" />
            or sign in with your password
            <span className="h-px flex-1 bg-border" />
          </div>
        </div>
      )}

      <form
        onSubmit={handleSubmit(requires2FA ? handle2FA : onSubmit)}
        className="space-y-5"
      >
        {/* Email Field */}
        <div>
          <label
            htmlFor="email"
            className="block text-sm font-medium text-foreground mb-2"
          >
            Email Address
          </label>
          <Input
            id="email"
            type="email"
            placeholder="you@company.com"
            {...register("email")}
            disabled={isLoading}
          />
          {errors.email && (
            <p className="mt-2 text-sm font-medium text-destructive">
              {errors.email.message}
            </p>
          )}
        </div>

        {/* Password Field */}
        <div>
          <label
            htmlFor="password"
            className="block text-sm font-medium text-foreground mb-2"
          >
            Password
          </label>
          <div className="relative">
            <Input
              id="password"
              type={showPassword ? "text" : "password"}
              placeholder="Enter your password"
              className="pr-12"
              {...register("password")}
              disabled={isLoading}
            />
            <button
              type="button"
              onClick={() => setShowPassword(!showPassword)}
              className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
              tabIndex={-1}
            >
              {showPassword ? (
                <EyeOff className="w-5 h-5" />
              ) : (
                <Eye className="w-5 h-5" />
              )}
            </button>
          </div>
          {errors.password && (
            <p className="mt-2 text-sm font-medium text-destructive">
              {errors.password.message}
            </p>
          )}
        </div>

        {/* 2FA Field - Conditional */}
        {requires2FA && (
          <div className="animate-in fade-in slide-in-from-top-2 duration-300">
            <div className="flex items-center gap-2 mb-2">
              <ShieldCheck className="w-4 h-4 text-muted-foreground" />
              <label
                htmlFor="totpCode"
                className="block text-sm font-medium text-foreground"
              >
                Two-Factor Code
              </label>
            </div>
            <Input
              id="totpCode"
              type="text"
              inputMode="numeric"
              pattern="[0-9]*"
              maxLength={6}
              placeholder="123456"
              className="tracking-widest text-center"
              {...register("totpCode")}
              disabled={isLoading}
            />
            <p className="mt-2 text-xs text-muted-foreground">
              Enter the 6-digit code from your authenticator app
            </p>
            {errors.totpCode && (
              <p className="mt-2 text-sm font-medium text-destructive">
                {errors.totpCode.message}
              </p>
            )}
          </div>
        )}

        {/* Submit Button */}
        <Button
          type="submit"
          size="xl"
          className="w-full text-base font-medium"
          disabled={isLoading}
        >
          {isLoading ? (
            <>
              <Loader2 className="w-5 h-5 animate-spin mr-2" />
              <span>Verifying...</span>
            </>
          ) : requires2FA ? (
            "Verify"
          ) : (
            "Sign In"
          )}
        </Button>
      </form>
      </>
      )}

      {/* Footer Links */}
      <div className="mt-6 text-center">
        <p className="text-xs text-muted-foreground">
          Having trouble signing in?{" "}
          <a href="/forgot-password" className="font-medium text-primary hover:underline">
            Reset password
          </a>
        </p>
      </div>
    </div>
  )
}