'use client';

import { useParams } from 'next/navigation';
import Link from 'next/link';
import { ArrowLeft } from 'lucide-react';
import { VNCConsole } from '@/components/vnc-console';

export default function ClientConsolePage() {
  const params = useParams();
  const vmId = params.id as string;

  return (
    <div className="space-y-3">
      <Link
        href={`/client/vms/${vmId}`}
        className="inline-flex items-center gap-2 h-9 px-3 border-2 border-black bg-white font-bold uppercase text-xs"
      >
        <ArrowLeft className="w-4 h-4" /> Back to VM
      </Link>
      <div className="h-[calc(100vh-160px)] border-4 border-black bg-black">
        <VNCConsole vmId={vmId} className="h-full" />
      </div>
    </div>
  );
}
