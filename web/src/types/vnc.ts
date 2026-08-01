// VNCConfig matches the GET /api/vms/:id/vnc response (data field).
//
// The WebSocket endpoint is returned as a relative, token-bearing path
// (ws_path) that stays same-origin. The browser derives the absolute ws:// or
// wss:// URL from window.location so the internal panel host (e.g.
// panel:8080) is never exposed to the client bundle.
export interface VNCConfig {
  vm_id: string;
  host: string;
  port: number;
  password?: string;
  ws_path?: string;
  // Legacy field kept for type-compat only. Modern servers omit it; if present
  // it MUST NOT contain a foreign/absolute host (see vnc-console.tsx).
  websocket_url?: string;
}

// buildVNCWsUrl safely turns a relative ws_path into an absolute ws:// or wss://
// URL using the page's own origin. It rejects any path that is not strictly
// same-origin (absolute URL, protocol-relative, or path traversal), so a
// malicious/legacy response cannot redirect the socket to a foreign origin.
export function buildVNCWsUrl(path: string): string {
  const trimmed = path.trim();
  if (trimmed === '') {
    throw new Error('Empty VNC WebSocket path');
  }

  // Reject absolute URLs (http/https/ws/wss) and protocol-relative (//host).
  if (/^[a-z][a-z0-9+.-]*:/i.test(trimmed) || trimmed.startsWith('//')) {
    throw new Error(`Refusing foreign-origin VNC WebSocket URL: ${trimmed}`);
  }

  // Resolve against the page origin and verify it stays on the same origin.
  const abs = new URL(trimmed, window.location.href);
  if (abs.origin !== window.location.origin) {
    throw new Error(`Refusing cross-origin VNC WebSocket URL: ${abs.href}`);
  }

  // Keep the path same-origin; only the scheme flips to ws/wss.
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}${abs.pathname}${abs.search}${abs.hash}`;
}
