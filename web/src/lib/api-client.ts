import axios, { AxiosError, AxiosInstance, AxiosRequestConfig } from 'axios'

const API_BASE_URL = process.env.NEXT_PUBLIC_API_URL ?? 'http://localhost:8089'

export interface ApiError {
  status: number
  message: string
  errors?: Record<string, string[]>
}

function getCookie(name: string): string | null {
  if (typeof document === 'undefined') return null
  const value = `; ${document.cookie}`
  const parts = value.split(`; ${name}=`)
  if (parts.length === 2) return parts.pop()?.split(';').shift() ?? null
  return null
}

function deleteCookie(name: string): void {
  if (typeof document === 'undefined') return
  document.cookie = `${name}=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;`
}

function createApiClient(): AxiosInstance {
  const client = axios.create({
    baseURL: API_BASE_URL,
    withCredentials: true,
    headers: {
      'Content-Type': 'application/json',
    },
  })

  client.interceptors.request.use(
    (config) => {
      const token = getCookie('refreshToken')
      if (token && config.headers) {
        config.headers.Authorization = `Bearer ${token}`
      }
      return config
    },
    (error) => Promise.reject(error)
  )

  client.interceptors.response.use(
    (response) => response,
    (error: AxiosError<ApiError>) => {
      if (error.response) {
        const status = error.response.status
        const data = error.response.data

        if (status === 401) {
          deleteCookie('refreshToken')
          if (typeof window !== 'undefined') {
            window.location.href = '/login'
          }
        }

        const apiError: ApiError = {
          status,
          message: data?.message ?? error.message,
          errors: data?.errors,
        }

        return Promise.reject(apiError)
      }

      const apiError: ApiError = {
        status: 500,
        message: error.message ?? 'Network error',
      }

      return Promise.reject(apiError)
    }
  )

  return client
}

const apiClient = createApiClient()

export const api = {
  get: <T>(url: string, params?: object) =>
    apiClient.get<T>(url, { params }).then((r) => r.data),
  post: <T>(url: string, data?: unknown) =>
    apiClient.post<T>(url, data).then((r) => r.data),
  put: <T>(url: string, data?: unknown) =>
    apiClient.put<T>(url, data).then((r) => r.data),
  patch: <T>(url: string, data?: unknown) =>
    apiClient.patch<T>(url, data).then((r) => r.data),
  delete: <T>(url: string) => apiClient.delete<T>(url).then((r) => r.data),
}

export function createServerApiClient(token?: string): AxiosInstance {
  const client = axios.create({
    baseURL: API_BASE_URL,
    headers: {
      'Content-Type': 'application/json',
    },
  })

  if (token) {
    client.defaults.headers.common['Authorization'] = `Bearer ${token}`
  }

  return client
}

export const serverApi = {
  get: <T>(client: AxiosInstance, url: string, params?: object) =>
    client.get<T>(url, { params }).then((r) => r.data),
  post: <T>(client: AxiosInstance, url: string, data?: unknown) =>
    client.post<T>(url, data).then((r) => r.data),
  put: <T>(client: AxiosInstance, url: string, data?: unknown) =>
    client.put<T>(url, data).then((r) => r.data),
  patch: <T>(client: AxiosInstance, url: string, data?: unknown) =>
    client.patch<T>(url, data).then((r) => r.data),
  delete: <T>(client: AxiosInstance, url: string) =>
    client.delete<T>(url).then((r) => r.data),
}
