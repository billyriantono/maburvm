export interface PaginatedResponse<T> {
  data: T[]
  total: number
  page: number
  pageSize: number
  totalPages?: number
}

export interface ApiResponse<T> {
  data: T
  message?: string
}

export interface ValidationError {
  field: string
  message: string
}

export interface ValidationErrors {
  errors: ValidationError[]
}
