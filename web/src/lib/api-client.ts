import axios, { AxiosInstance, AxiosRequestConfig, AxiosResponse } from 'axios';
import { apiErrorCode, apiErrorMessage } from './api-error';

// API Response wrapper matching Go backend structure
export interface ApiResponse<T> {
  data: T;
  message?: string;
  success: boolean;
}

export type { ApiError, PageMeta } from '@/types/api';

// Create axios instance with default config.
// Same-origin: requests to /api are proxied server-side to the panel via
// next.config.js rewrites(), so the panel hostname never reaches the client.
const apiClient: AxiosInstance = axios.create({
  baseURL: '',
  headers: {
    'Content-Type': 'application/json',
  },
  timeout: 30000,
});

// The session is an HttpOnly cookie the panel sets at sign-in. The browser
// sends it with every same-origin request; no script can read it.

// Pages a dropped session must not bounce away from.
const SIGN_IN_PATHS = ['/login', '/forgot-password', '/reset-password'];

// sessionEnded goes to sign-in, coming back here after. The panel's 401 has
// already removed the cookie. There is no refresh: the session lasts the
// operator's session length (Settings, System, Security) or until revoked.
function sessionEnded() {
  if (typeof window === 'undefined') return;
  const { pathname, search } = window.location;
  if (SIGN_IN_PATHS.some((p) => pathname === p || pathname.startsWith(p + '/'))) return;
  window.location.href = '/login?next=' + encodeURIComponent(pathname + search);
}

// The profile page is where two-factor is turned on.
const TWO_FACTOR_SETUP_PATH = '/settings/profile';

apiClient.interceptors.response.use(
  (response: AxiosResponse) => response,
  (error) => {
    // Give every failure a sentence worth showing. Pages put error.message
    // straight into a toast, and axios's own "Request failed with status code
    // 502" tells the person nothing about what happened or what to do.
    const status: number | undefined = error.response?.status;
    const body = error.response?.data;
    const fromServer = apiErrorMessage(body);
    const code = apiErrorCode(body);
    if (status === 503 && code === 'maintenance' && fromServer) {
      // Maintenance mode: the panel is up and says why the change waits.
      error.message = fromServer;
    } else if (!error.response || status === 502 || status === 503 || status === 504) {
      error.message = 'The panel did not answer. It may be restarting. Try again in a moment.';
    } else if (fromServer) {
      error.message = fromServer;
    }

    if (status === 401) {
      sessionEnded();
    } else if (
      status === 403 &&
      code === 'two_factor_required' &&
      typeof window !== 'undefined' &&
      window.location.pathname !== TWO_FACTOR_SETUP_PATH
    ) {
      // The operator requires two-factor for administrators: set it up first.
      window.location.href = TWO_FACTOR_SETUP_PATH;
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
