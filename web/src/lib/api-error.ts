// Reading the panel's error answers. Every one is
//   {success: false, error: {code, message, details?}, message, ...}
// and panels from before that envelope sent {error: "text"} or
// {error: "Title", message: "text"}, sometimes with a top-level code.

type ErrorBody = {
  message?: unknown
  code?: unknown
  error?: unknown
} | null | undefined

function errorObject(body: ErrorBody): { code?: unknown; message?: unknown } | null {
  const e = body?.error
  return e && typeof e === 'object' ? (e as { code?: unknown; message?: unknown }) : null
}

/** What went wrong, in words, or fallback when the body says nothing usable. */
export function apiErrorMessage(body: unknown, fallback = ''): string {
  const b = body as ErrorBody
  if (typeof b?.message === 'string' && b.message) return b.message
  const obj = errorObject(b)
  if (typeof obj?.message === 'string' && obj.message) return obj.message
  if (typeof b?.error === 'string' && b.error) return b.error
  return fallback
}

/** The machine code of an error answer ("maintenance", "captcha_required"...), or "". */
export function apiErrorCode(body: unknown): string {
  const b = body as ErrorBody
  if (typeof b?.code === 'string' && b.code) return b.code
  const obj = errorObject(b)
  return typeof obj?.code === 'string' ? obj.code : ''
}
