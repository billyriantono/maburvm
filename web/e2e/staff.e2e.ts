import { test, expect } from "@playwright/test"
import { customerID, stateFile } from "./fixtures"

test.describe("a support account", () => {
  test.use({ storageState: stateFile("support") })

  test("does not reach the system settings", async ({ page }) => {
    await page.goto("/dashboard")
    const nav = page.getByRole("complementary").getByRole("navigation")
    await expect(nav.getByRole("link", { name: "Virtual Machines" })).toBeVisible()
    await expect(nav.getByRole("link", { name: "System", exact: true })).toHaveCount(0)
    await expect(nav.getByRole("link", { name: "Branding" })).toHaveCount(0)

    // Typing the address does not get around it: the panel refuses.
    const refused = await page.request.get("/api/v1/settings/system")
    expect(refused.status()).toBe(403)
    expect(await refused.text()).toMatch(/permission/i)
    await page.goto("/settings/system")
    await expect(page.getByText("Insufficient permissions")).toBeVisible({ timeout: 30_000 })
  })

  test("cannot view the panel as a customer", async ({ page }) => {
    await page.goto(`/users/${customerID}`)
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible()
    await expect(page.getByRole("button", { name: "View as customer" })).toHaveCount(0)
    const refused = await page.request.post(`/api/v1/users/${customerID}/impersonate`)
    expect(refused.status()).toBe(403)
    expect(await refused.text()).toMatch(/permission/i)
  })
})

test.describe("the owner", () => {
  test.use({ storageState: stateFile("owner") })

  test("sees the system settings", async ({ page }) => {
    await page.goto("/dashboard")
    await expect(page.getByRole("complementary").getByRole("link", { name: "System", exact: true })).toBeVisible()
  })
})
