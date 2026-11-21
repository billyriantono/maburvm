import { describe, expect, it } from "vitest"
import { reachLabel } from "./team"
import { fillScript, missingInputs } from "./recipes"
import { addonPrices, planAllowances } from "./plans"

describe("reachLabel", () => {
  it("says every server when nothing was chosen", () => {
    expect(reachLabel({ vm_ids: [], project_ids: [] }, [])).toBe("Every server")
  })
  it("counts chosen servers", () => {
    expect(reachLabel({ vm_ids: ["a"], project_ids: [] }, [])).toBe("1 server")
    expect(reachLabel({ vm_ids: ["a", "b"], project_ids: [] }, [])).toBe("2 servers")
  })
  it("names projects it knows, counts the rest", () => {
    expect(reachLabel({ vm_ids: [], project_ids: ["p"] }, [{ id: "p", name: "Shop" }])).toBe("Projects: Shop")
    expect(reachLabel({ vm_ids: [], project_ids: ["p", "q"] }, [{ id: "p", name: "Shop" }])).toBe("2 projects")
  })
})

describe("recipe inputs", () => {
  const inputs = [
    { key: "domain", label: "Domain", required: true },
    { key: "port", label: "Port", required: true, default: "80" },
    { key: "token", label: "Token", required: false, secret: true },
  ]
  it("lists required inputs left empty, a default counting as filled", () => {
    expect(missingInputs(inputs, {})).toEqual(["Domain"])
    expect(missingInputs(inputs, { domain: " " })).toEqual(["Domain"])
    expect(missingInputs(inputs, { domain: "example.com" })).toEqual([])
  })
  it("fills placeholders and hides secrets", () => {
    expect(fillScript("serve {{domain}}:{{port}} {{token}}", inputs, { domain: "example.com", token: "s3cret" })).toBe("serve example.com:80 ••••")
  })
})

describe("plan extras", () => {
  it("lists the new allowances", () => {
    expect(planAllowances({ max_snapshots: null, max_backups: null, max_extra_ipv4: null, max_extra_ipv6: null, max_extra_disks: 2, max_images: 0, max_floating_ips: 1 }))
      .toEqual(["2 extra disks", "No saved images", "1 floating IP"])
  })
  it("prices the extras in order, only when a currency is set", () => {
    const rows = addonPrices({ currency: "USD", addon_prices: { floating_ip: 3, snapshot: 0.5 } })
    expect(rows.map((r) => r.key)).toEqual(["snapshot", "floating_ip"])
    expect(rows[1].price).toContain("3")
    expect(addonPrices({ currency: "", addon_prices: { snapshot: 1 } })).toEqual([])
  })
})
