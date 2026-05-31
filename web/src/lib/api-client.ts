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
  baseURL: process.env.NEXT_PUBLIC_API_URL || '',
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

// Response interceptor: Handle 401 and token refresh
let isRefreshing = false;
let failedQueue: Array<{ resolve: (token: string) => void; reject: (error: unknown) => void }> = [];

const processQueue = (error: unknown, token: string | null = null) => {
  failedQueue.forEach(prom => {
    if (error) {
      prom.reject(error);
    } else {
      prom.resolve(token!);
    }
  });
  failedQueue = [];
};

apiClient.interceptors.response.use(
  (response: AxiosResponse) => response,
  async (error) => {
    const originalRequest = error.config;

    if (error.response?.status === 401 && !originalRequest._retry) {
      // Don't retry refresh token requests
      if (originalRequest.url?.includes('/auth/refresh')) {
        // Refresh failed — clear tokens and redirect
        if (typeof document !== 'undefined') {
          document.cookie = 'accessToken=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;';
          document.cookie = 'refreshToken=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;';
        }
        if (typeof window !== 'undefined') {
          window.location.href = '/login';
        }
        return Promise.reject(error);
      }

      if (isRefreshing) {
        return new Promise((resolve, reject) => {
          failedQueue.push({ resolve, reject });
        }).then(token => {
          originalRequest.headers['Authorization'] = 'Bearer ' + token;
          return apiClient(originalRequest);
        }).catch(err => Promise.reject(err));
      }

      originalRequest._retry = true;
      isRefreshing = true;

      try {
        // Try to refresh the token
        const refreshToken = typeof document !== 'undefined'
          ? document.cookie.split('; ').find(row => row.startsWith('refreshToken='))?.split('=')[1]
          : null;

        if (!refreshToken) {
          throw new Error('No refresh token');
        }

        const response = await apiClient.post('/api/v1/auth/refresh', { refresh_token: refreshToken });
        const newToken = response.data?.data?.access_token || response.data?.access_token;

        if (newToken && typeof document !== 'undefined') {
          document.cookie = `accessToken=${newToken}; path=/;`;
          originalRequest.headers['Authorization'] = 'Bearer ' + newToken;
          processQueue(null, newToken);
          return apiClient(originalRequest);
        }

        throw new Error('No token in refresh response');
      } catch (refreshError) {
        processQueue(refreshError, null);
        // Clear tokens and redirect to login
        if (typeof document !== 'undefined') {
          document.cookie = 'accessToken=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;';
          document.cookie = 'refreshToken=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;';
        }
        if (typeof window !== 'undefined') {
          window.location.href = '/login';
        }
        return Promise.reject(refreshError);
      } finally {
        isRefreshing = false;
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
