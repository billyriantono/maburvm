'use client';

import { useParams } from 'next/navigation';
import { VNCConsole } from '@/components/vnc-console';

export default function ConsolePage() {
  const params = useParams();
  const vmId = params.id as string;

  return (
    <div className="h-[calc(100vh-64px)]">
      <VNCConsole
        vmId={vmId}
        className="h-full"
        onConnect={() => console.log('VNC connected')}
        onDisconnect={(clean) => console.log('VNC disconnected', clean ? '(clean)' : '(unclean)')}
        onError={(msg) => console.error('VNC error:', msg)}
      />
    </div>
  );
}
