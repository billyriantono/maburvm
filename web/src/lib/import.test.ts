import { describe, expect, it } from "vitest"
import { actionable, billingLinksCsv, formatBillingRefs, importBatches, parseBillingRefs } from "./import"
import type { ImportItem, ImportPlan } from "@/types/import"

describe("parseBillingRefs", () => {
  it("reads one VM per line, with or without a header", () => {
    expect(parseBillingRefs("vpsid,serviceid\n37,1234\n38\twhmcs-77\n\n# note\n45 9001\n")).toEqual({
      refs: { "37": "1234", "38": "whmcs-77", "45": "9001" },
      errors: [],
    })
  })

  it("says which lines it cannot read", () => {
    const { refs, errors } = parseBillingRefs("37,1234\nweb,12\n40\n")
    expect(refs).toEqual({ "37": "1234" })
    expect(errors).toEqual([
      "Line 2: the first value must be the source VM ID, a number.",
      "Line 3: VM 40 has no billing reference after it.",
    ])
  })

  it("writes back what it read, in VM order", () => {
    expect(formatBillingRefs({ "100": "a", "9": "b" })).toBe("9,b\n100,a")
    expect(parseBillingRefs(formatBillingRefs({ "100": "a", "9": "b" })).refs).toEqual({ "100": "a", "9": "b" })
  })
})

const item = (kind: ImportItem["kind"], key: string, action: ImportItem["action"] = "create"): ImportItem => ({ kind, key, label: key, action })

describe("importBatches", () => {
  it("orders pools, plans, accounts and VMs, one pool at a time", () => {
    const items = [item("vm", "1"), item("vm", "2"), item("vm", "3"), item("user", "9"), item("pool", "1"), item("pool", "2"), item("plan", "5"), item("vm", "4", "blocked")]
    expect(importBatches(items, actionable, 2)).toEqual([
      [{ kind: "pool", key: "1" }],
      [{ kind: "pool", key: "2" }],
      [{ kind: "plan", key: "5" }],
      [{ kind: "user", key: "9" }],
      [{ kind: "vm", key: "1" }, { kind: "vm", key: "2" }],
      [{ kind: "vm", key: "3" }],
    ])
  })
})

describe("billingLinksCsv", () => {
  it("lists each imported VM with its new IDs, guarded against formulas", () => {
    const plan = {
      items: [
        { ...item("user", "55", "exists"), target_id: "u-1", target_label: "ana@example.com" },
        { ...item("vm", "37", "exists"), target_id: "vm-1", vm: { owner_key: "55", owner_label: "ana@example.com", hostname: "=web, one", billing_ref: "whmcs-1234" } },
        { ...item("vm", "38", "create"), vm: { owner_key: "55", owner_label: "ana@example.com", hostname: "db" } },
      ],
    } as unknown as ImportPlan
    expect(billingLinksCsv(plan)).toBe(
      "source_vm_id,hostname,vm_id,owner_email,owner_user_id,billing_ref\n37,\"'=web, one\",vm-1,ana@example.com,u-1,whmcs-1234\n",
    )
  })
})
