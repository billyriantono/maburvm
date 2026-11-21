// Generic API response wrapper matching Go backend
export interface ApiResponse<T> {
  data: T;
  message?: string;
  success: boolean;
}

// The error object of every error answer:
// {success: false, error: ApiError, message: error.message}.
export interface ApiError {
  code: string;
  message: string;
  details?: unknown;
}

// The pagination block every paginated list carries beside its data.
export interface PageMeta {
  page: number;
  per_page: number;
  total: number;
}

// Pagination parameters
export interface PaginationParams {
  page?: number;
  limit?: number;
  sort?: string;
  order?: 'asc' | 'desc';
}

// Paginated response. meta is the one shape every paginated list carries;
// the other fields are the older ones some lists still send beside it.
export interface PaginatedResponse<T> {
  data: T[];
  meta?: PageMeta;
  total: number;
  page: number;
  limit: number;
  totalPages: number;
}
