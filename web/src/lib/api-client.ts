import axios, { AxiosInstance, AxiosRequestConfig, AxiosResponse } from 'axios';

// API Response wrapper matching Go backend structure
export interface ApiResponse<T> {
  data: T;
  message?: string;
  success: boolean;
}

// API Error structure
export interface ApiError {
  message: string;
  code?: string;
  details?: Record<string, string[]>;
}

// Create axios instance with default config
const apiClient: AxiosInstance = axios.create({
  baseURL: process.env.NEXT_PUBLIC_API_URL || 'http://localhost:8080',
  headers: {
    'Content-Type': 'application/json',
  },
  timeout: 30000,
});

// Request interceptor: Attach JWT token
apiClient.interceptors.request.use(
  (config) => {
    // Try to get token from cookie first, then localStorage
    let token: string | null = null;
    
    if (typeof document !== 'undefined') {
      // Try cookie first (preferred for httpOnly)
      const cookieMatch = document.cookie.match(/accessToken=([^;]+)/);
      if (cookieMatch) {
        token = cookieMatch[1];
      } else {
        // Fallback to localStorage
        token = localStorage.getItem('accessToken');
      }
    }

    if (token) {
      config.headers.Authorization = `Bearer ${token}`;
    }

    return config;
  },
  (error) => {
    return Promise.reject(error);
  }
);

// Response interceptor: Handle 401 and other errors
apiClient.interceptors.response.use(
  (response: AxiosResponse) => response,
  (error) => {
    if (error.response?.status === 401) {
      // Clear tokens
      if (typeof document !== 'undefined') {
        document.cookie = 'accessToken=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;';
        document.cookie = 'refreshToken=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;';
        localStorage.removeItem('accessToken');
        localStorage.removeItem('refreshToken');
      }
      
      // Redirect to login if in browser
      if (typeof window !== 'undefined') {
        window.location.href = '/login';
      }
    }
    
    return Promise.reject(error);
  }
);

// Typed HTTP methods
export const api = {
  get: <T>(url: string, config?: AxiosRequestConfig): Promise<AxiosResponse<ApiResponse<T>>> =>
    apiClient.get<ApiResponse<T>>(url, config),

  post: <T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<AxiosResponse<ApiResponse<T>>> =>
    apiClient.post<ApiResponse<T>>(url, data, config),

  put: <T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<AxiosResponse<ApiResponse<T>>> =>
    apiClient.put<ApiResponse<T>>(url, data, config),

  patch: <T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<AxiosResponse<ApiResponse<T>>> =>
    apiClient.patch<ApiResponse<T>>(url, data, config),

  delete: <T>(url: string, config?: AxiosRequestConfig): Promise<AxiosResponse<ApiResponse<T>>> =>
    apiClient.delete<ApiResponse<T>>(url, config),
};

export default apiClient;
export { apiClient };
