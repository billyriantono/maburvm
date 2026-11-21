// contentSecurityPolicy allows what the pages use and nothing else: scripts,
// styles, fonts and images from this site (fonts are self-hosted by
// next/font; logos are data: URLs), and API calls plus the console
// websockets to this same host. Next.js and the theme switcher inline small
// bootstrap scripts, hence 'unsafe-inline'; development also needs eval.
export function contentSecurityPolicy(host: string, production: boolean): string {
  const sockets = /^[a-z0-9.\-:[\]]+$/i.test(host) ? ` wss://${host} ws://${host}` : ''
  return [
    "default-src 'self'",
    `script-src 'self' 'unsafe-inline'${production ? '' : " 'unsafe-eval'"}`,
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data: blob:",
    "font-src 'self' data:",
    `connect-src 'self'${sockets}`,
    "object-src 'none'",
    "base-uri 'self'",
    "form-action 'self'",
    "frame-ancestors 'none'",
  ].join('; ')
}
