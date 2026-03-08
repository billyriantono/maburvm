// Generic API response wrapper matching Go backend
export interface ApiResponse<T> {
  data: T;
  message?: string;
  success: boolean;
}

// API Error response
export interface ApiError {
  message: string;
  code?: string;
  details?: Record<string, string[]>;
}

// Pagination parameters
export interface PaginationParams {
  page?: number;
  limit?: number;
  sort?: string;
  order?: 'asc' | 'desc';
}

// Paginated response
export interface PaginatedResponse<T> {
  data: T[];
  total: number;
  page: number;
  limit: number;
  totalPages: number;
}
