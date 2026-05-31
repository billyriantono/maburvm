"use client"

import { useState } from "react"
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
      const response = await fetch("/api/v1/auth/login", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(data),
      })

      const result = await response.json()

      if (!response.ok) {
        // Handle IP whitelist error (403)
        if (response.status === 403 && result.code === "IP_NOT_WHITELISTED") {
          setIpWarning({
            message: result.message || "Your IP address is not whitelisted",
            code: result.code,
            userIP: result.userIP,
          })
          return
        }

        // Handle 2FA requirement
        if (response.status === 200 && result.requires2FA) {
          setRequires2FA(true)
          return
        }

        // Handle other errors
        setError({
          message: result.message || "Login failed. Please try again.",
          code: result.code,
        })
        return
      }

      // Success - store token and redirect to dashboard
      document.cookie = `accessToken=${result.token}; path=/; max-age=${24 * 60 * 60}; SameSite=Lax`
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
      const response = await fetch("/api/v1/auth/login", {
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
        setError({
          message: result.message || "Invalid verification code",
          code: result.code,
        })
        return
      }

      // Success - store token and redirect to dashboard
      document.cookie = `accessToken=${result.token}; path=/; max-age=${24 * 60 * 60}; SameSite=Lax`
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
      <h2 className="text-2xl font-black uppercase tracking-tight mb-6 text-black">
        Sign In
      </h2>

      {/* IP Whitelist Warning */}
      {ipWarning && (
        <div className="mb-6 p-4 bg-amber-50 border-2 border-amber-500 rounded-lg">
          <div className="flex items-start gap-3">
            <AlertTriangle className="w-5 h-5 text-amber-600 shrink-0 mt-0.5" />
            <div>
              <p className="font-bold text-amber-800 text-sm uppercase tracking-wide">
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
        <div className="mb-6 p-4 bg-red-50 border-2 border-red-500 rounded-lg">
          <div className="flex items-start gap-3">
            <AlertTriangle className="w-5 h-5 text-red-600 shrink-0 mt-0.5" />
            <p className="font-bold text-red-700 text-sm">{error.message}</p>
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
            className="block text-xs font-black uppercase tracking-wider text-gray-700 mb-2"
          >
            Email Address
          </label>
          <Input
            id="email"
            type="email"
            placeholder="you@company.com"
            className="text-lg font-medium border-2 border-black placeholder:text-gray-600 focus:border-black focus:ring-0"
            {...register("email")}
            disabled={isLoading}
          />
          {errors.email && (
            <p className="mt-2 text-sm font-bold text-red-600">
              {errors.email.message}
            </p>
          )}
        </div>

        {/* Password Field */}
        <div>
          <label
            htmlFor="password"
            className="block text-xs font-black uppercase tracking-wider text-gray-700 mb-2"
          >
            Password
          </label>
          <div className="relative">
            <Input
              id="password"
              type={showPassword ? "text" : "password"}
              placeholder="Enter your password"
              className="text-lg font-medium border-2 border-black placeholder:text-gray-600 focus:border-black focus:ring-0 pr-12"
              {...register("password")}
              disabled={isLoading}
            />
            <button
              type="button"
              onClick={() => setShowPassword(!showPassword)}
              className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-500 hover:text-black transition-colors"
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
            <p className="mt-2 text-sm font-bold text-red-600">
              {errors.password.message}
            </p>
          )}
        </div>

        {/* 2FA Field - Conditional */}
        {requires2FA && (
          <div className="animate-in fade-in slide-in-from-top-2 duration-300">
            <div className="flex items-center gap-2 mb-2">
              <ShieldCheck className="w-4 h-4 text-black" />
              <label
                htmlFor="totpCode"
                className="block text-xs font-black uppercase tracking-wider text-gray-700"
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
              className="text-lg font-medium border-2 border-black placeholder:text-gray-600 focus:border-black focus:ring-0 tracking-widest text-center"
              {...register("totpCode")}
              disabled={isLoading}
            />
            <p className="mt-2 text-xs text-gray-500">
              Enter the 6-digit code from your authenticator app
            </p>
            {errors.totpCode && (
              <p className="mt-2 text-sm font-bold text-red-600">
                {errors.totpCode.message}
              </p>
            )}
          </div>
        )}

        {/* Submit Button */}
        <Button
          type="submit"
          className="w-full h-12 text-base font-black uppercase tracking-wider border-2 border-black bg-black text-white hover:bg-gray-900 active:translate-x-0.5 active:translate-y-0.5 active:shadow-none transition-all"
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

      {/* Footer Links */}
      <div className="mt-6 text-center">
        <p className="text-xs text-gray-500">
          Having trouble signing in?{" "}
          <a href="/forgot-password" className="font-bold text-black hover:underline">
            Reset password
          </a>
        </p>
      </div>
    </div>
  )
}