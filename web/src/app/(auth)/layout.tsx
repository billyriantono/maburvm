"use client"

import { useBranding } from "@/lib/branding/provider"
import { SignInFrame, usePanelStatus } from "@/components/sign-in-frame"

export default function AuthLayout({ children }: { children: React.ReactNode }) {
  const { branding } = useBranding()
  const [status, recheck] = usePanelStatus()
  return (
    <SignInFrame b={branding} status={status} onRecheck={recheck}>
      {children}
    </SignInFrame>
  )
}
