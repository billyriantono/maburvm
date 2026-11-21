// contentSecurityPolicy allows what the pages use and nothing else: scripts,
// styles, fonts and images from this site (fonts are self-hosted by
// next/font; logos are data: URLs), and API calls plus the console
// websockets to this same host. Next.js and the theme switcher inline small
// bootstrap scripts, hence 'unsafe-inline'; development also needs eval.
// captcha adds the sign-in check's providers (Turnstile, hCaptcha), for the
// sign-in and password reset pages only.
export function contentSecurityPolicy(host: string, production: boolean, captcha = false): string {
  const sockets = /^[a-z0-9.\-:[\]]+$/i.test(host) ? ` wss://${host} ws://${host}` : ''
  const turnstile = 'https://challenges.cloudflare.com'
  const hcaptcha = 'https://hcaptcha.com https://*.hcaptcha.com'
  const extra = (sources: string) => (captcha ? ` ${sources}` : '')
  return [
    "default-src 'self'",
    `script-src 'self' 'unsafe-inline'${production ? '' : " 'unsafe-eval'"}${extra(`${turnstile} ${hcaptcha}`)}`,
    `style-src 'self' 'unsafe-inline'${extra(hcaptcha)}`,
    "img-src 'self' data: blob:",
    "font-src 'self' data:",
    `connect-src 'self'${sockets}${extra(`${turnstile} ${hcaptcha}`)}`,
    `frame-src 'self'${extra(`${turnstile} ${hcaptcha}`)}`,
    "object-src 'none'",
    "base-uri 'self'",
    "form-action 'self'",
    "frame-ancestors 'none'",
  ].join('; ')
}
