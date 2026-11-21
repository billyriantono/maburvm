import { describe, expect, it } from 'vitest';
import { contentSecurityPolicy } from './security-headers';

describe('contentSecurityPolicy', () => {
  it('lets the console websockets reach this host and nothing else', () => {
    const csp = contentSecurityPolicy('panel.example.com', true);
    expect(csp).toContain("connect-src 'self' wss://panel.example.com ws://panel.example.com");
    expect(csp).toContain("frame-ancestors 'none'");
    expect(csp).toContain("object-src 'none'");
    expect(csp).not.toContain('unsafe-eval');
  });

  it('allows eval only in development, for React refresh', () => {
    expect(contentSecurityPolicy('localhost:3000', false)).toContain("'unsafe-eval'");
  });

  it('lets the captcha providers in on the sign-in pages only', () => {
    const plain = contentSecurityPolicy('panel.example.com', true);
    expect(plain).not.toContain('challenges.cloudflare.com');
    expect(plain).not.toContain('hcaptcha.com');
    expect(plain).toContain("frame-src 'self';");
    const signIn = contentSecurityPolicy('panel.example.com', true, true);
    expect(signIn).toContain("script-src 'self' 'unsafe-inline' https://challenges.cloudflare.com https://hcaptcha.com https://*.hcaptcha.com");
    expect(signIn).toContain("frame-src 'self' https://challenges.cloudflare.com https://hcaptcha.com https://*.hcaptcha.com");
    expect(signIn).toContain("frame-ancestors 'none'");
  });

  it('never copies a strange Host header into the policy', () => {
    const csp = contentSecurityPolicy('evil.com; script-src *', true);
    expect(csp).toContain("connect-src 'self';");
    expect(csp).not.toContain('evil.com');
  });
});
