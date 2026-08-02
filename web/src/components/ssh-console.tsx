'use client';

import { useEffect, useRef, useState, useCallback } from 'react';
import type { Terminal as XTerm } from '@xterm/xterm';
import type { FitAddon as XFitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';

interface SSHConsoleProps {
  vmId: string;
  className?: string;
}

type Status = 'auth' | 'connecting' | 'connected' | 'disconnected' | 'error';

function getCookie(name: string): string | undefined {
  return document.cookie
    .split(';')
    .map((c) => c.trim())
    .find((c) => c.startsWith(name + '='))
    ?.split('=')[1];
}

function buildWsUrl(apiBase: string, path: string, token: string): string {
  // apiBase is intentionally empty: the WebSocket must stay same-origin and
  // be routed through the /ws rewrite so the panel host never reaches the
  // client bundle. We therefore always derive host/proto from window.location.
  const base = apiBase ? new URL(apiBase) : new URL(window.location.href);
  const proto = base.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${base.host}${path}?token=${encodeURIComponent(token)}`;
}

export function SSHConsole({ vmId, className }: SSHConsoleProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<XTerm | null>(null);
  const fitRef = useRef<XFitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [status, setStatus] = useState<Status>('auth');
  const [errorMessage, setErrorMessage] = useState('');
  const [username, setUsername] = useState('root');
  const [password, setPassword] = useState('');

  const sendResize = useCallback((ws: WebSocket, term: XTerm) => {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
    }
  }, []);

  const connect = useCallback(async () => {
    setStatus('connecting');
    setErrorMessage('');

    try {
      const auth = getCookie('accessToken');
      // Same-origin: /api and /ws are proxied server-side to the panel via
      // next.config.js rewrites(); the panel host never reaches the client.
      const apiBase = '';

      const resp = await fetch(`/api/v1/vms/${vmId}/ssh/token`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${auth}` },
        body: JSON.stringify({ username, password }),
      });
      if (!resp.ok) {
        const e = await resp.json().catch(() => ({}));
        throw new Error(e.message || e.error || `Failed to start SSH session (${resp.status})`);
      }
      const json = await resp.json();
      const data = json.data || json;
      const wsUrl = buildWsUrl(apiBase, data.ws_path || '/ws/ssh', data.token);

      // Load xterm at runtime (avoids any SSR/import-time window access).
      const { Terminal } = await import('@xterm/xterm');
      const { FitAddon } = await import('@xterm/addon-fit');

      const container = containerRef.current;
      if (!container) {
        throw new Error('Terminal container is not ready');
      }

      const term = new Terminal({
        cursorBlink: true,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        fontSize: 13,
        theme: { background: '#000000' },
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      container.replaceChildren();
      term.open(container);
      fit.fit();
      termRef.current = term;
      fitRef.current = fit;

      const ws = new WebSocket(wsUrl);
      ws.binaryType = 'arraybuffer';
      wsRef.current = ws;

      ws.onopen = () => {
        setStatus('connected');
        sendResize(ws, term);
        term.focus();
      };
      ws.onmessage = (ev) => {
        if (typeof ev.data === 'string') {
          try {
            const msg = JSON.parse(ev.data);
            if (msg.type === 'error') {
              setErrorMessage(msg.message || 'SSH error');
              setStatus('error');
              return;
            }
          } catch {
            // not JSON — treat as terminal output
          }
          term.write(ev.data);
        } else {
          term.write(new Uint8Array(ev.data));
        }
      };
      ws.onclose = () => setStatus((s) => (s === 'error' ? s : 'disconnected'));
      ws.onerror = () => {
        setStatus('error');
        setErrorMessage('WebSocket connection error');
      };

      term.onData((d) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(new TextEncoder().encode(d));
        }
      });
      term.onResize(() => sendResize(ws, term));
    } catch (err) {
      setStatus('error');
      setErrorMessage((err as Error).message);
    }
  }, [vmId, username, password, sendResize]);

  // Refit on window resize.
  useEffect(() => {
    const onResize = () => {
      try {
        fitRef.current?.fit();
      } catch {
        // terminal not ready
      }
    };
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  // Cleanup on unmount.
  useEffect(() => {
    return () => {
      wsRef.current?.close();
      termRef.current?.dispose();
    };
  }, []);

  const disconnect = () => {
    wsRef.current?.close();
    termRef.current?.dispose();
    termRef.current = null;
    wsRef.current = null;
    setStatus('auth');
  };

  return (
    <div className={className}>
      {/* Status bar */}
      <div className="flex items-center justify-between px-4 py-2 bg-card text-card-foreground border-b">
        <div className="flex items-center gap-3">
          <span className="text-sm font-semibold">SSH Console</span>
          <span className="text-xs text-muted-foreground">{vmId.slice(0, 8)}</span>
        </div>
        <div className="flex items-center gap-2">
          <StatusBadge status={status} />
          {(status === 'disconnected' || status === 'error') && (
            <button
              onClick={() => setStatus('auth')}
              className="px-3 py-1 text-xs font-medium border rounded-md bg-background hover:bg-muted cursor-pointer"
            >
              Reconnect
            </button>
          )}
          {status === 'connected' && (
            <button
              onClick={disconnect}
              className="px-3 py-1 text-xs font-medium border rounded-md bg-background hover:bg-muted cursor-pointer"
            >
              Disconnect
            </button>
          )}
        </div>
      </div>

      {/* Body */}
      <div className="relative bg-black" style={{ height: 'calc(100% - 42px)' }}>
        {/* Terminal container is always mounted so its ref is available the moment
            connect() runs (the auth form is just an overlay on top of it). */}
        <div ref={containerRef} className="w-full h-full p-1" />

        {status === 'auth' && (
          <div className="absolute inset-0 flex items-center justify-center p-6">
            <form
              onSubmit={(e) => {
                e.preventDefault();
                connect();
              }}
              className="w-full max-w-sm bg-card text-card-foreground border rounded-lg shadow-sm p-6 space-y-4"
            >
              <div>
                <h2 className="text-lg font-semibold">Connect via SSH</h2>
                <p className="text-xs text-muted-foreground mt-1">
                  Credentials are used once to open the session and are not stored.
                </p>
              </div>
              <div>
                <label htmlFor="ssh-user" className="block text-xs font-medium text-muted-foreground mb-1">
                  Username
                </label>
                <input
                  id="ssh-user"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="w-full rounded-md border border-input bg-background p-2 font-mono text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                  autoComplete="username"
                />
              </div>
              <div>
                <label htmlFor="ssh-pass" className="block text-xs font-medium text-muted-foreground mb-1">
                  Password
                </label>
                <input
                  id="ssh-pass"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full rounded-md border border-input bg-background p-2 font-mono text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                  autoComplete="current-password"
                  autoFocus
                />
              </div>
              <button
                type="submit"
                disabled={!password}
                className="w-full px-4 py-2 text-sm font-medium rounded-md bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
              >
                Connect
              </button>
            </form>
          </div>
        )}

        {status !== 'auth' && status !== 'connected' && (
          <div className="absolute inset-0 flex items-center justify-center text-white text-center pointer-events-none">
            <div>
              <h2 className="text-xl font-semibold mb-2">
                {status === 'connecting' && 'Connecting...'}
                {status === 'disconnected' && 'Disconnected'}
                {status === 'error' && 'Connection Error'}
              </h2>
              <p className="text-sm text-gray-400">
                {status === 'connecting' && 'Opening SSH session'}
                {status === 'disconnected' && 'Session ended'}
                {status === 'error' && errorMessage}
              </p>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  const styles: Record<string, string> = {
    auth: 'border-sky-200 bg-sky-50 text-sky-700 dark:bg-sky-950 dark:text-sky-300 dark:border-sky-900',
    connecting: 'border-amber-200 bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900',
    connected: 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900',
    disconnected: 'border bg-muted text-muted-foreground',
    error: 'border-red-200 bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-300 dark:border-red-900',
  };
  return (
    <span className={`inline-flex items-center rounded-md px-2 py-0.5 text-[11px] font-medium border capitalize ${styles[status]}`}>
      {status}
    </span>
  );
}
