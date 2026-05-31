'use client';

import { useEffect, useRef, useState, useCallback } from 'react';

interface VNCConsoleProps {
  vmId: string;
  className?: string;
  onConnect?: () => void;
  onDisconnect?: (clean: boolean) => void;
  onError?: (message: string) => void;
}

export function VNCConsole({ vmId, className, onConnect, onDisconnect, onError }: VNCConsoleProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const rfbRef = useRef<any>(null);
  const [status, setStatus] = useState<'connecting' | 'connected' | 'disconnected' | 'error'>('connecting');
  const [errorMessage, setErrorMessage] = useState('');

  const connect = useCallback(async () => {
    if (!containerRef.current) return;

    setStatus('connecting');
    setErrorMessage('');

    try {
      // Fetch VNC config from API
      const token = document.cookie
        .split(';')
        .map(c => c.trim())
        .find(c => c.startsWith('accessToken='))
        ?.split('=')[1];

      const resp = await fetch(`/api/v1/vms/${vmId}/vnc`, {
        headers: { Authorization: `Bearer ${token}` },
      });

      if (!resp.ok) {
        throw new Error(`Failed to get VNC config: ${resp.statusText}`);
      }

      const json = await resp.json();
      const config = json.data || json;

      if (!config.websocket_url) {
        throw new Error('No WebSocket URL returned');
      }

      // Dynamically import noVNC RFB from public path
      // @ts-expect-error noVNC is served from /public at runtime, outside TS module resolution.
      const { default: RFB } = await import(/* webpackIgnore: true */ '/novnc/core/rfb.js');

      // Clear container
      containerRef.current.innerHTML = '';

      // Create RFB connection
      const rfb = new RFB(containerRef.current, config.websocket_url, {
        credentials: { password: config.password || '' },
        wsProtocols: ['binary'],
      });

      rfb.scaleViewport = true;
      rfb.resizeSession = true;

      // Enable clipboard
      rfb.clipViewport = true;

      rfb.addEventListener('connect', () => {
        setStatus('connected');
        onConnect?.();
      });

      rfb.addEventListener('disconnect', (e: any) => {
        setStatus('disconnected');
        onDisconnect?.(e.detail.clean);
      });

      rfb.addEventListener('securityfailure', (e: any) => {
        setStatus('error');
        setErrorMessage(e.detail.reason || 'Authentication failed');
        onError?.(e.detail.reason);
      });

      // Handle clipboard from remote VM → browser
      rfb.addEventListener('clipboard', (e: any) => {
        if (e.detail.text && navigator.clipboard) {
          navigator.clipboard.writeText(e.detail.text).catch(() => {
            // Clipboard write failed (permissions)
          });
        }
      });

      rfbRef.current = rfb;
    } catch (err: any) {
      setStatus('error');
      setErrorMessage(err.message);
      onError?.(err.message);
    }
  }, [vmId, onConnect, onDisconnect, onError]);

  // Handle paste from browser → remote VM
  useEffect(() => {
    const handlePaste = (e: ClipboardEvent) => {
      if (rfbRef.current && status === 'connected') {
        const text = e.clipboardData?.getData('text');
        if (text) {
          rfbRef.current.clipboardPasteFrom(text);
        }
      }
    };

    // Also handle focus-based clipboard sync
    const handleFocus = async () => {
      if (rfbRef.current && status === 'connected' && navigator.clipboard) {
        try {
          const text = await navigator.clipboard.readText();
          if (text) {
            rfbRef.current.clipboardPasteFrom(text);
          }
        } catch {
          // Clipboard read failed (permissions)
        }
      }
    };

    document.addEventListener('paste', handlePaste);
    window.addEventListener('focus', handleFocus);

    return () => {
      document.removeEventListener('paste', handlePaste);
      window.removeEventListener('focus', handleFocus);
    };
  }, [status]);

  useEffect(() => {
    connect();

    return () => {
      if (rfbRef.current) {
        rfbRef.current.disconnect();
        rfbRef.current = null;
      }
    };
  }, [connect]);

  return (
    <div className={className}>
      {/* Status bar */}
      <div className="flex items-center justify-between px-4 py-2 bg-white border-b-2 border-black">
        <div className="flex items-center gap-3">
          <span className="text-sm font-bold">VM CONSOLE</span>
          <span className="text-xs text-gray-500">{vmId.slice(0, 8)}</span>
        </div>
        <div className="flex items-center gap-2">
          <StatusBadge status={status} />
          {(status === 'disconnected' || status === 'error') && (
            <button
              onClick={connect}
              className="px-3 py-1 text-xs font-bold border-2 border-black bg-white hover:bg-gray-100 cursor-pointer"
            >
              RETRY
            </button>
          )}
        </div>
      </div>

      {/* Console viewport */}
      <div className="relative bg-black" style={{ height: 'calc(100% - 42px)' }}>
        <div ref={containerRef} className="w-full h-full" />

        {/* Overlay for non-connected states */}
        {status !== 'connected' && (
          <div className="absolute inset-0 flex items-center justify-center text-white text-center">
            <div>
              <h2 className="text-xl font-bold mb-2">
                {status === 'connecting' && 'Connecting...'}
                {status === 'disconnected' && 'Disconnected'}
                {status === 'error' && 'Connection Error'}
              </h2>
              <p className="text-sm text-gray-600">
                {status === 'connecting' && 'Establishing VNC connection'}
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
