import { NextResponse } from 'next/server'
import type { NextRequest } from 'next/server'

// Whitelisted paths that don't require authentication
const WHITELIST = ['/login', '/reset-password']

export function middleware(request: NextRequest) {
  const { pathname } = request.nextUrl
  
  // Allow API routes and static files
  if (pathname.startsWith('/api/') || pathname.startsWith('/_next/')) {
    return NextResponse.next()
  }
  
  // Check for access token
  const token = request.cookies.get('accessToken')
  
  const isDashboard = pathname.startsWith('/dashboard')
  const isLogin = pathname === '/login'

  // Root redirect — send to dashboard (which will bounce to login if no token)
  if (pathname === '/') {
    if (token) {
      return NextResponse.redirect(new URL('/dashboard', request.url))
    }
    return NextResponse.redirect(new URL('/login', request.url))
  }

  // Protect dashboard routes
  if (isDashboard && !token) {
    return NextResponse.redirect(new URL('/login', request.url))
  }
  
  // Redirect authenticated users away from login
  if (isLogin && token) {
    return NextResponse.redirect(new URL('/dashboard', request.url))
  }
  
  return NextResponse.next()
}

export const config = {
  matcher: ['/((?!_next/static|_next/image|favicon.ico).*)'],
}
