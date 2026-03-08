'use client'

import {
  createContext,
  ReactNode,
  useCallback,
  useContext,
  useEffect,
  useState,
} from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api-client'
import type { User, LoginRequest, LoginResponse } from '@/types'

interface AuthContextType {
  user: User | null
  isAuthenticated: boolean
  isLoading: boolean
  login: (email: string, password: string, totpCode?: string) => Promise<void>
  logout: () => Promise<void>
  error: string | null
}

const AuthContext = createContext<AuthContextType | undefined>(undefined)

function setCookie(name: string, value: string, days: number): void {
  const expires = new Date()
  expires.setTime(expires.getTime() + days * 24 * 60 * 60 * 1000)
  document.cookie = `${name}=${value};expires=${expires.toUTCString()};path=/;Secure;SameSite=Strict`
}

function deleteCookie(name: string): void {
  document.cookie = `${name}=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;`
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const queryClient = useQueryClient()

  const checkAuth = useCallback(async () => {
    try {
      const response = await api.get<User>('/api/v1/auth/me')
      setUser(response.data.data)
    } catch {
      setUser(null)
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    checkAuth()
  }, [checkAuth])

  const login = useCallback(
    async (email: string, password: string, totpCode?: string) => {
      setError(null)
      try {
        const response = await api.post<LoginResponse>('/api/v1/auth/login', {
          email,
          password,
          totp_code: totpCode,
        } as LoginRequest)

        const loginData = response.data.data

        if (loginData.access_token) {
          setCookie('accessToken', loginData.access_token, 7)
        }
        if (loginData.refresh_token) {
          setCookie('refreshToken', loginData.refresh_token, 7)
        }

        setUser(loginData.user)
      } catch (err) {
        const errorMessage =
          err instanceof Error ? err.message : 'Login failed'
        setError(errorMessage)
        throw err
      }
    },
    []
  )

  const logout = useCallback(async () => {
    try {
      await api.post('/api/v1/auth/logout')
    } catch {
      // Ignore logout errors
    } finally {
      deleteCookie('accessToken')
      deleteCookie('refreshToken')
      setUser(null)
      queryClient.clear()
      window.location.href = '/login'
    }
  }, [queryClient])

  return (
    <AuthContext.Provider
      value={{
        user,
        isAuthenticated: !!user,
        isLoading,
        login,
        logout,
        error,
      }}
    >
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextType {
  const context = useContext(AuthContext)
  if (context === undefined) {
    throw new Error('useAuth must be used within an AuthProvider')
  }
  return context
}
