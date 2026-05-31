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
    if (!containerRef.current) return;
    setStatus('connecting');
    setErrorMessage('');

    try {
      const auth = getCookie('accessToken');
      const apiBase = process.env.NEXT_PUBLIC_API_URL || '';

      const resp = await fetch(`${apiBase}/api/v1/vms/${vmId}/ssh/token`, {
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

      const term = new Terminal({
        cursorBlink: true,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        fontSize: 13,
        theme: { background: '#000000' },
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      containerRef.current.replaceChildren();
      term.open(containerRef.current);
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
      <div className="flex items-center justify-between px-4 py-2 bg-white border-b-2 border-black">
        <div className="flex items-center gap-3">
          <span className="text-sm font-bold">SSH CONSOLE</span>
          <span className="text-xs text-gray-500">{vmId.slice(0, 8)}</span>
        </div>
        <div className="flex items-center gap-2">
          <StatusBadge status={status} />
          {(status === 'disconnected' || status === 'error') && (
            <button
              onClick={() => setStatus('auth')}
              className="px-3 py-1 text-xs font-bold border-2 border-black bg-white hover:bg-gray-100 cursor-pointer"
            >
              RECONNECT
            </button>
          )}
          {status === 'connected' && (
            <button
              onClick={disconnect}
              className="px-3 py-1 text-xs font-bold border-2 border-black bg-white hover:bg-gray-100 cursor-pointer"
            >
              DISCONNECT
            </button>
          )}
        </div>
      </div>

      {/* Body */}
      <div className="relative bg-black" style={{ height: 'calc(100% - 42px)' }}>
        {status === 'auth' ? (
          <div className="absolute inset-0 flex items-center justify-center p-6">
            <form
              onSubmit={(e) => {
                e.preventDefault();
                connect();
              }}
              className="w-full max-w-sm bg-white border-2 border-black p-6 space-y-4"
            >
              <div>
                <h2 className="text-lg font-black uppercase tracking-tight">Connect via SSH</h2>
                <p className="text-xs text-gray-500 mt-1">
                  Credentials are used once to open the session and are not stored.
                </p>
              </div>
              <div>
                <label htmlFor="ssh-user" className="block text-xs font-bold uppercase tracking-wider text-gray-500 mb-1">
                  Username
                </label>
                <input
                  id="ssh-user"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="w-full border-2 border-black p-2 font-mono text-sm focus:outline-none focus:ring-2 focus:ring-primary"
                  autoComplete="username"
                />
              </div>
              <div>
                <label htmlFor="ssh-pass" className="block text-xs font-bold uppercase tracking-wider text-gray-500 mb-1">
                  Password
                </label>
                <input
                  id="ssh-pass"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full border-2 border-black p-2 font-mono text-sm focus:outline-none focus:ring-2 focus:ring-primary"
                  autoComplete="current-password"
                  autoFocus
                />
              </div>
              <button
                type="submit"
                disabled={!password}
                className="w-full px-4 py-2 text-sm font-bold uppercase border-2 border-black bg-primary hover:brightness-95 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
              >
                Connect
              </button>
            </form>
          </div>
        ) : (
          <>
            <div ref={containerRef} className="w-full h-full p-1" />
            {status !== 'connected' && (
              <div className="absolute inset-0 flex items-center justify-center text-white text-center pointer-events-none">
                <div>
                  <h2 className="text-xl font-bold mb-2">
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
          </>
        )}
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  const styles: Record<string, string> = {
    auth: 'border-blue-600 bg-blue-50 text-blue-800',
    connecting: 'border-yellow-600 bg-yellow-50 text-yellow-800',
    connected: 'border-green-600 bg-green-50 text-green-800',
    disconnected: 'border-gray-500 bg-gray-100 text-gray-700',
    error: 'border-red-600 bg-red-50 text-red-800',
  };
  return (
    <span className={`inline-flex items-center px-2 py-0.5 text-[11px] font-bold border ${styles[status]}`}>
      {status.toUpperCase()}
    </span>
  );
}
