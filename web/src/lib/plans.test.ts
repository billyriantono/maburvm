import { describe, expect, it } from "vitest"
import { backupTerms } from "./plans"

describe("backupTerms", () => {
  it("says what the plan allows and charges", () => {
    expect(backupTerms({ max_backups: null, addon_prices: null, currency: "" })).toBeNull()
    expect(backupTerms({ max_backups: 0, addon_prices: null, currency: "" })).toBe("This server's plan includes no backups.")
    expect(backupTerms({ max_backups: 1, addon_prices: null, currency: "" })).toBe("This server's plan keeps up to 1 backup.")
    expect(backupTerms({ max_backups: 3, addon_prices: { backup: 2 }, currency: "USD" })).toMatch(/^This server's plan keeps up to 3 backups, at .*2.* a month each\.$/)
    expect(backupTerms({ max_backups: null, addon_prices: { backup: 2 }, currency: "USD" })).toMatch(/^Each backup costs .*2.* a month\.$/)
    expect(backupTerms({ max_backups: 3, addon_prices: { backup: 2 }, currency: "" })).toBe("This server's plan keeps up to 3 backups.")
  })
})
