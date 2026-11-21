import { test, expect } from "@playwright/test"
import { stateFile } from "./fixtures"

test.use({ storageState: stateFile("owner") })

// The whole create path: the wizard, the panel's job, the node building the
// disk (the fake agent here), and the page following the VM to running.
test("an administrator creates a VM from a plan", async ({ page }) => {
  const hostname = `web-${Date.now().toString(36)}`
  await page.goto("/vms/new")

  await page.getByLabel("Hostname *").fill(hostname)
  await page.getByRole("combobox", { name: /Target Node/ }).click()
  await page.getByRole("option", { name: /e2e-node/ }).click()
  await page.getByRole("button", { name: "Next", exact: true }).click()

  await page.getByRole("combobox", { name: "Plan (optional)" }).click()
  await page.getByRole("option", { name: /E2E Starter/ }).click()
  await expect(page.getByText("1 vCPU", { exact: true })).toBeVisible()
  await page.getByRole("button", { name: "Next", exact: true }).click()

  await page.getByRole("button", { name: /Debian/ }).click()
  await page.getByRole("button", { name: "Next", exact: true }).click()

  await page.getByRole("combobox", { name: /IP Pool/ }).click()
  await page.getByRole("option", { name: /e2e-public/ }).click()
  await page.getByRole("button", { name: "Next", exact: true }).click()

  await expect(page.getByRole("heading", { name: "Review & Create" })).toBeVisible()
  // The plan's size, not the sliders' defaults.
  for (const [label, value] of [["CPU:", "1 vCPU"], ["RAM:", "1 GB"], ["Disk:", "20 GB"]]) {
    await expect(page.getByRole("main").getByText(label, { exact: true }).locator("..")).toContainText(value)
  }
  await page.getByRole("button", { name: "Create VM" }).click()

  await expect(page.getByRole("heading", { name: `${hostname} is ready` })).toBeVisible({ timeout: 45_000 })
  await expect(page.getByRole("heading", { name: "Password for root" })).toBeVisible()

  await page.getByRole("button", { name: "Open VM" }).click()
  // The dev server compiles the VM page on its first visit, which can take a while.
  await expect(page).toHaveURL(/\/vms\/[0-9a-f-]{36}$/, { timeout: 60_000 })
  await expect(page.getByRole("heading", { level: 1, name: hostname })).toBeVisible()
  await expect(page.getByRole("main").getByText("Running", { exact: true }).first()).toBeVisible()
  // The address came from the pool the plan's node offers.
  await expect(page.getByRole("main").getByText(/^192\.0\.2\.\d+$/).first()).toBeVisible()
})
