import { describe, expect, it } from "vitest"
import { describeRule } from "./firewall"
import { planAllowances } from "./plans"

describe("describeRule", () => {
  it("reads as a sentence", () => {
    expect(describeRule({ action: "allow", protocol: "tcp", port_range: "80,443", direction: "inbound", source_ip: "0.0.0.0/0" })).toBe(
      "Allow TCP 80,443 inbound from anywhere",
    )
    expect(describeRule({ action: "deny", protocol: "icmp", icmp_type: "", direction: "outbound", source_ip: "10.0.0.0/8", family: "v4" })).toBe(
      "Drop ICMP (all types) outbound to 10.0.0.0/8, IPv4 only",
    )
    expect(describeRule({ action: "reject", protocol: "all", direction: "inbound", source_ip: "" })).toBe("Reject all traffic inbound from anywhere")
  })
})

describe("planAllowances", () => {
  it("names a NAT plan's ports and hostnames", () => {
    const base = { max_snapshots: null, max_backups: null, max_extra_ipv4: null, max_extra_ipv6: null }
    expect(planAllowances({ ...base, network_type: "nat", nat_ports: 20, nat_hostnames: 1 })).toEqual([
      "Private address with 20 public ports",
      "1 website hostname",
    ])
    expect(planAllowances({ ...base, network_type: "public", nat_ports: 20 })).toEqual([])
  })
})
