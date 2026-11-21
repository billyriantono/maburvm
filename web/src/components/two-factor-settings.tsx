"use client"

import { useEffect, useState } from "react"
import { toast } from "sonner"
import QRCode from "qrcode"
import { AlertTriangle, Check, Copy, Loader2, Shield } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { StatusBadge } from "@/components/status-badge"
import { useBranding } from "@/lib/branding/provider"
import { useCurrentUser } from "@/lib/hooks/use-auth"
import { useDisable2FA, useSetup2FA, useVerify2FA } from "@/lib/hooks/use-settings"
import { useCopy } from "@/lib/use-feedback"

type Step = "qr" | "verify" | "backup"

// TwoFactorSettings turns authenticator-app two-factor on and off for the
// signed-in account, administrator or customer alike.
export function TwoFactorSettings() {
  const { branding } = useBranding()
  const { data: user } = useCurrentUser()
  const setup = useSetup2FA()
  const verify = useVerify2FA()
  const disable = useDisable2FA()
  const { copy, copied } = useCopy()

  const enabled = !!user?.two_factor_enabled
  const [setupOpen, setSetupOpen] = useState(false)
  const [step, setStep] = useState<Step>("qr")
  const [secret, setSecret] = useState("")
  const [backupCodes, setBackupCodes] = useState<string[]>([])
  const [qr, setQr] = useState<{ size: number; path: string } | null>(null)
  const [code, setCode] = useState("")
  const [disableOpen, setDisableOpen] = useState(false)
  const [disableCode, setDisableCode] = useState("")

  // Drawn as vector squares on whole-unit coordinates: a resampled bitmap
  // smears the squares enough that apps reading it off a screen fail.
  useEffect(() => {
    if (!secret || !user) return
    const issuer = encodeURIComponent(branding.name)
    const url = `otpauth://totp/${issuer}:${encodeURIComponent(user.email)}?secret=${secret}&issuer=${issuer}`
    try {
      const { modules } = QRCode.create(url, { errorCorrectionLevel: "M" })
      let d = ""
      for (let y = 0; y < modules.size; y++)
        for (let x = 0; x < modules.size; x++) if (modules.data[y * modules.size + x]) d += `M${x} ${y}h1v1h-1z`
      setQr({ size: modules.size, path: d })
    } catch {
      setQr(null)
      toast.error("Could not draw the QR code. Enter the key below in your app instead.")
    }
  }, [secret, user, branding.name])

  const start = async () => {
    setStep("qr")
    setSecret("")
    setQr(null)
    setCode("")
    setSetupOpen(true)
    try {
      const res = await setup.mutateAsync()
      setSecret(res.secret)
      setBackupCodes(res.backup_codes || [])
    } catch (err) {
      toast.error("Two-factor could not be set up", { description: (err as Error).message })
      setSetupOpen(false)
    }
  }

  const confirm = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await verify.mutateAsync(code.trim())
      setStep("backup")
      toast.success("Two-factor is on")
    } catch (err) {
      toast.error("Code not accepted", { description: (err as Error).message })
    }
  }

  const closeSetup = () => {
    setSetupOpen(false)
    setBackupCodes([])
    setSecret("")
    setCode("")
  }

  const turnOff = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      await disable.mutateAsync(disableCode.trim())
      setDisableOpen(false)
      setDisableCode("")
      toast.success("Two-factor is off")
    } catch (err) {
      toast.error("Two-factor is still on", { description: (err as Error).message })
    }
  }

  // The codes as a text file, made in the browser: they are never sent anywhere.
  const downloadCodes = () => {
    const lines = [
      `${branding.name} backup codes for ${user?.email ?? "your account"}`,
      `Saved ${new Date().toLocaleString()}`,
      "",
      "Each code signs you in once, in place of the code from your authenticator app.",
      "Keep this file somewhere only you can reach.",
      "",
      ...backupCodes,
      "",
    ]
    const url = URL.createObjectURL(new Blob([lines.join("\n")], { type: "text/plain;charset=utf-8" }))
    const a = document.createElement("a")
    a.href = url
    a.download = `${branding.name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "panel"}-backup-codes.txt`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  return (
    <Card>
      <CardHeader className="border-b">
        <CardTitle className="flex items-center gap-2">
          <Shield className="w-5 h-5" aria-hidden="true" />
          Two-factor authentication
        </CardTitle>
        <CardDescription>A code from an authenticator app, asked for after your password.</CardDescription>
      </CardHeader>
      <CardContent className="p-6 space-y-4">
        {user?.two_factor_setup_required && (
          <div role="alert" className="rounded-md border border-warning/50 bg-warning/10 p-4 text-sm">
            <p className="font-semibold">Turn on two-factor to continue</p>
            <p className="mt-1">This panel requires administrators to use two-factor authentication. The rest of the panel opens once it is on.</p>
          </div>
        )}
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="space-y-1">
            <StatusBadge status={enabled ? "on" : "off"} variant="lamp" />
            <p className="text-sm text-muted-foreground">
              {enabled
                ? "Backup codes were shown once, when you turned it on. Lost them? Turn two-factor off and on again for a new set."
                : "Without it, your password alone signs in to this account."}
            </p>
          </div>
          {enabled ? (
            <Button variant="outline" onClick={() => { setDisableCode(""); setDisableOpen(true) }}>
              Turn off
            </Button>
          ) : (
            <Button onClick={start} disabled={setup.isPending}>
              {setup.isPending && <Loader2 className="w-4 h-4 mr-2 animate-spin" aria-hidden="true" />}
              Turn on two-factor
            </Button>
          )}
        </div>
      </CardContent>

      <Dialog open={setupOpen} onOpenChange={(open) => !open && closeSetup()}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>
              {step === "qr" && "Add this account to your authenticator app"}
              {step === "verify" && "Enter the code from your app"}
              {step === "backup" && "Save your backup codes"}
            </DialogTitle>
            <DialogDescription>
              {step === "qr" && "Scan the code with an app such as an authenticator or password manager."}
              {step === "verify" && "Two-factor turns on once the app's 6-digit code is accepted."}
              {step === "backup" && "Each code signs you in once if you lose your phone. They are not shown again."}
            </DialogDescription>
          </DialogHeader>

          {step === "qr" && (
            <div className="flex flex-col items-center space-y-4">
              {/* White in both themes, with the four-square quiet zone scanners expect. */}
              <div className="rounded-md border bg-white p-2">
                {qr ? (
                  <svg role="img" aria-label="QR code for your authenticator app" viewBox={`-4 -4 ${qr.size + 8} ${qr.size + 8}`} shapeRendering="crispEdges" className="h-64 w-64">
                    <rect x={-4} y={-4} width={qr.size + 8} height={qr.size + 8} fill="#ffffff" />
                    <path d={qr.path} fill="#000000" />
                  </svg>
                ) : (
                  <div className="flex h-64 w-64 items-center justify-center text-black">
                    <Loader2 className="h-8 w-8 animate-spin" aria-label="Drawing the QR code" />
                  </div>
                )}
              </div>
              <div className="text-center">
                <p className="text-sm font-medium">Cannot scan it? Enter this key in the app instead</p>
                <p className="mt-1.5 flex flex-wrap items-center justify-center gap-2">
                  <code className="break-all rounded-md border bg-muted px-2 py-1 font-mono text-xs">{secret || "…"}</code>
                  {secret && (
                    <Button type="button" variant="ghost" size="sm" onClick={() => copy(secret, "secret")}>
                      {copied("secret") ? "Copied" : "Copy"}
                    </Button>
                  )}
                </p>
              </div>
              <Button onClick={() => setStep("verify")} className="w-full" disabled={!secret}>
                Next
              </Button>
            </div>
          )}

          {step === "verify" && (
            <form onSubmit={confirm} className="space-y-4">
              <div>
                <label htmlFor="two-factor-code" className="block text-sm font-medium text-muted-foreground mb-2">
                  6-digit code
                </label>
                <Input
                  id="two-factor-code"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  autoFocus
                  maxLength={6}
                  placeholder="123456"
                  className="text-center text-2xl font-mono tracking-widest"
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                />
              </div>
              <DialogFooter>
                <Button type="button" variant="ghost" onClick={() => setStep("qr")}>Back</Button>
                <Button type="submit" disabled={verify.isPending || code.length !== 6}>
                  {verify.isPending && <Loader2 className="w-4 h-4 mr-2 animate-spin" aria-hidden="true" />}
                  Turn on two-factor
                </Button>
              </DialogFooter>
            </form>
          )}

          {step === "backup" && (
            <div className="space-y-4">
              <div className="grid grid-cols-2 gap-2 rounded-md border bg-muted p-4">
                {backupCodes.map((c) => (
                  <code key={c} className="rounded-md border bg-background px-2 py-1 text-center font-mono text-xs">{c}</code>
                ))}
              </div>
              <p className="flex items-center gap-2 text-sm font-medium text-destructive">
                <AlertTriangle className="w-4 h-4 shrink-0" aria-hidden="true" />
                Save these now. They are not shown again.
              </p>
              <div className="flex gap-2">
                <Button variant="secondary" className="flex-1" onClick={() => copy(backupCodes.join("\n"), "codes")}>
                  {copied("codes") ? <Check className="w-4 h-4 mr-2" aria-hidden="true" /> : <Copy className="w-4 h-4 mr-2" aria-hidden="true" />}
                  {copied("codes") ? "Copied" : "Copy codes"}
                </Button>
                <Button variant="secondary" className="flex-1" onClick={downloadCodes}>
                  Download as a text file
                </Button>
              </div>
              <Button onClick={closeSetup} className="w-full">Done</Button>
            </div>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={disableOpen} onOpenChange={setDisableOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Turn off two-factor?</DialogTitle>
            <DialogDescription>
              Your password alone will sign in to this account. Enter a new code from your authenticator app, or one of your backup codes, to confirm.
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={turnOff} className="space-y-4">
            <div>
              <label htmlFor="two-factor-disable-code" className="block text-sm font-medium text-muted-foreground mb-2">
                Code from your app, or a backup code
              </label>
              <Input
                id="two-factor-disable-code"
                autoComplete="one-time-code"
                autoFocus
                maxLength={8}
                className="text-center font-mono tracking-widest"
                value={disableCode}
                onChange={(e) => setDisableCode(e.target.value.trim())}
              />
            </div>
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setDisableOpen(false)}>Cancel</Button>
              <Button type="submit" variant="destructive" disabled={disable.isPending || (disableCode.length !== 6 && disableCode.length !== 8)}>
                {disable.isPending && <Loader2 className="w-4 h-4 mr-2 animate-spin" aria-hidden="true" />}
                Turn off
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
