import type { FirewallGroupRule } from "@/types"

/** One line a person can read: "Allow TCP 80,443 inbound from anywhere". */
export function describeRule(r: Pick<FirewallGroupRule, "action" | "protocol" | "port_range" | "direction" | "source_ip" | "icmp_type" | "family">) {
  const what = r.protocol === "all" ? "all traffic" : r.protocol === "icmp" ? `ICMP ${r.icmp_type || "(all types)"}` : `${r.protocol.toUpperCase()} ${r.port_range || "(all ports)"}`
  const remote = !r.source_ip || r.source_ip === "0.0.0.0/0" ? "anywhere" : r.source_ip
  const family = r.family === "v4" ? ", IPv4 only" : r.family === "v6" ? ", IPv6 only" : ""
  const verb = r.action === "allow" ? "Allow" : r.action === "reject" ? "Reject" : "Drop"
  return `${verb} ${what} ${r.direction} ${r.direction === "inbound" ? "from" : "to"} ${remote}${family}`
}
