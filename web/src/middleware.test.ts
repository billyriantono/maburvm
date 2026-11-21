import { describe, expect, it } from 'vitest';
import { NextRequest } from 'next/server';
import { middleware } from './middleware';

const req = (path: string, method = 'GET', cookie?: string) =>
  new NextRequest(new URL(path, 'https://panel.example.com'), {
    method,
    headers: cookie ? { cookie } : {},
  });

describe('middleware', () => {
  it('passes the billing webhook and node installer through to the panel', () => {
    for (const r of [req('/webhooks/billing', 'POST'), req('/install-agent.sh')]) {
      const res = middleware(r);
      expect(res.headers.get('location')).toBeNull();
      expect(res.headers.get('x-middleware-next')).toBe('1');
    }
  });

  it('sends a page without a session to sign-in, with security headers', () => {
    const res = middleware(req('/vms/1?tab=console'));
    expect(res.headers.get('location')).toBe('https://panel.example.com/login?next=%2Fvms%2F1%3Ftab%3Dconsole');
    expect(res.headers.get('x-frame-options')).toBe('DENY');
    expect(res.headers.get('content-security-policy')).toContain("frame-ancestors 'none'");
  });

  it('serves the first-run page without a session', () => {
    const res = middleware(req('/first-run'));
    expect(res.headers.get('location')).toBeNull();
    expect(res.headers.get('content-security-policy')).toContain("frame-ancestors 'none'");
  });

  it('lets a page with a session cookie through', () => {
    const res = middleware(req('/vms', 'GET', 'accessToken=t'));
    expect(res.headers.get('location')).toBeNull();
  });
});
