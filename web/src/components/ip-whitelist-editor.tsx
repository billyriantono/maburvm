"use client"

import { useEffect, useState } from "react"
import { Plus, Trash2, Globe, Lightbulb, Check, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Label } from "@/components/ui/label"
import { api } from "@/lib/api-client"

interface IPWhitelistEditorProps {
  value: string[]
  onChange: (ips: string[]) => void
  showSuggestion?: boolean
}

export function IPWhitelistEditor({ value = [], onChange, showSuggestion = true }: IPWhitelistEditorProps) {
  const [newIP, setNewIP] = useState("")
  const [error, setError] = useState("")
  const [suggestedIP, setSuggestedIP] = useState<string | null>(null)

  useEffect(() => {
    if (!showSuggestion) return
    let cancelled = false
    api
      .get<{ ip: string }>("/api/v1/auth/client-ip")
      .then((response) => {
        if (!cancelled) setSuggestedIP(response.data.data.ip)
      })
      .catch(() => {
        // Silently ignore — suggestion is a convenience, not a requirement.
      })
    return () => {
      cancelled = true
    }
  }, [showSuggestion])

  const validateIP = (ip: string): boolean => {
    // Simple validation for IP or CIDR
    const ipRegex = /^(\d{1,3}\.){3}\d{1,3}(\/\d{1,2})?$/
    if (!ipRegex.test(ip)) return false

    const parts = ip.split("/")[0].split(".")
    return parts.every((part) => {
      const num = parseInt(part, 10)
      return num >= 0 && num <= 255
    })
  }

  const handleAddIP = () => {
    if (!newIP.trim()) return

    if (!validateIP(newIP.trim())) {
      setError("Invalid IP address or CIDR format")
      return
    }

    if (value.includes(newIP.trim())) {
      setError("IP already in whitelist")
      return
    }

    onChange([...value, newIP.trim()])
    setNewIP("")
    setError("")
  }

  const handleRemoveIP = (ip: string) => {
    onChange(value.filter((i) => i !== ip))
  }

  const handleAddSuggested = () => {
    if (suggestedIP && !value.includes(suggestedIP)) {
      onChange([...value, suggestedIP])
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault()
      handleAddIP()
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <Label className="text-xs">IP Whitelist</Label>
        {showSuggestion && suggestedIP && (
          <Button
            variant="ghost"
            size="sm"
            onClick={handleAddSuggested}
            className="text-xs h-7"
            type="button"
          >
            <Lightbulb className="w-3 h-3 mr-1" />
            Add current IP ({suggestedIP})
          </Button>
        )}
      </div>

      {/* Current IPs */}
      {value.length > 0 && (
        <Card>
          <CardContent className="p-3">
            <div className="space-y-2">
              {value.map((ip) => (
                <div
                  key={ip}
                  className="flex items-center justify-between p-2 bg-gray-50 border-2 border-black"
                >
                  <div className="flex items-center gap-2">
                    <Globe className="w-4 h-4 text-gray-500" />
                    <span className="font-mono text-sm">{ip}</span>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => handleRemoveIP(ip)}
                    type="button"
                  >
                    <Trash2 className="w-3 h-3 text-danger" />
                  </Button>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Add new IP */}
      <div className="flex gap-2">
        <div className="flex-1">
          <Input
            placeholder="Enter IP or CIDR (e.g., 192.168.1.0/24)"
            value={newIP}
            onChange={(e) => {
              setNewIP(e.target.value)
              setError("")
            }}
            onKeyDown={handleKeyDown}
            className="font-mono"
          />
          {error && (
            <p className="text-xs text-danger mt-1 font-bold">{error}</p>
          )}
        </div>
        <Button onClick={handleAddIP} type="button" size="sm">
          <Plus className="w-4 h-4" />
        </Button>
      </div>

      {/* Help text */}
      <p className="text-xs text-gray-500">
        Leave empty to allow access from any IP. Use CIDR notation (e.g., 192.168.1.0/24) for ranges.
      </p>
    </div>
  )
}