"use client"

import { VMDetail } from "@/components/vm-detail"
import { useVMStatusStream } from "@/lib/hooks/use-vms"

export default function ClientVMDetailPage() {
  // Live status pushes, so a power action shows its result without a reload.
  useVMStatusStream()
  return <VMDetail mode="client" />
}
