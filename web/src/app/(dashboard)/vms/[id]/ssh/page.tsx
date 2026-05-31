'use client';

import { useParams } from 'next/navigation';
import { SSHConsole } from '@/components/ssh-console';

export default function SSHConsolePage() {
  const params = useParams();
  const vmId = params.id as string;

  return (
    <div className="h-[calc(100vh-64px)]">
      <SSHConsole vmId={vmId} className="h-full" />
    </div>
  );
}
