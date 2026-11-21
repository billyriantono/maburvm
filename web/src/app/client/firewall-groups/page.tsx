"use client"

import { ActingAccountScope } from "@/lib/acting-account"
import { FirewallGroupsManager } from "@/components/firewall-groups-manager"

function ClientFirewallGroupsPageBody() {
  return <FirewallGroupsManager mode="client" />
}

// A team member picks which account's resources the page shows.
export default function ClientFirewallGroupsPage() {
  return (
    <ActingAccountScope>
      <ClientFirewallGroupsPageBody />
    </ActingAccountScope>
  )
}
