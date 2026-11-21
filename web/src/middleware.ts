import { NextResponse } from 'next/server'
import type { NextRequest } from 'next/server'
import { contentSecurityPolicy } from './lib/security-headers'

// Paths that don't require authentication
const PUBLIC_PATHS = ['/login', '/forgot-password', '/reset-password']

export function middleware(request: NextRequest) {
  const { pathname } = request.nextUrl

  // Allow API routes and static files. API answers carry the panel's own headers.
  // The billing webhook and the node installer are panel paths too (rewrites in
  // next.config.js) called without a browser session; the panel checks them.
  if (pathname.startsWith('/api/') || pathname.startsWith('/_next/') ||
      pathname.startsWith('/webhooks/') || pathname === '/install-agent.sh') {
    return NextResponse.next()
  }

  return withSecurityHeaders(route(request), request)
}

function route(request: NextRequest): NextResponse {
  const { pathname } = request.nextUrl

  // The session is an HttpOnly cookie the panel sets; here it is only checked
  // for presence, the panel decides whether it is still good.
  const token = request.cookies.get('accessToken')

  const isPublic = PUBLIC_PATHS.some(p => pathname === p || pathname.startsWith(p + '/'))

  // Root redirect
  if (pathname === '/') {
    if (token) {
      return NextResponse.redirect(new URL('/dashboard', request.url))
    }
    return NextResponse.redirect(new URL('/login', request.url))
  }

  // Redirect authenticated users away from login
  if (isPublic && token) {
    return NextResponse.redirect(new URL('/dashboard', request.url))
  }

  // Protect all non-public routes — if no token, sign in and come back here
  if (!isPublic && !token) {
    const login = new URL('/login', request.url)
    login.searchParams.set('next', pathname + request.nextUrl.search)
    return NextResponse.redirect(login)
  }

  return NextResponse.next()
}

function withSecurityHeaders(res: NextResponse, request: NextRequest): NextResponse {
  const h = res.headers
  h.set('Content-Security-Policy', contentSecurityPolicy(request.headers.get('host') ?? '', process.env.NODE_ENV === 'production'))
  // Browsers ignore this over plain http, so it is safe to always send.
  h.set('Strict-Transport-Security', 'max-age=31536000')
  h.set('X-Frame-Options', 'DENY')
  h.set('X-Content-Type-Options', 'nosniff')
  h.set('Referrer-Policy', 'strict-origin-when-cross-origin')
  return res
}

export const config = {
  matcher: ['/((?!_next/static|_next/image|favicon.ico).*)'],
}
