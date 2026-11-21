import { test, expect } from "@playwright/test"
import { accounts, customerID, customerVM, stateFile } from "./fixtures"

test.use({ storageState: stateFile("owner") })

test("staff view the panel as a customer under a banner, and end it", async ({ page }) => {
  await page.goto(`/users/${customerID}`)
  await page.getByRole("button", { name: "View as customer" }).click()
  const confirm = page.getByRole("dialog", { name: `View the panel as ${accounts.customer.email}?` })
  await confirm.getByRole("button", { name: "View as customer" }).click()

  await expect(page).toHaveURL(/\/client\/dashboard/)
  const banner = page.getByRole("status").filter({ hasText: "You are viewing as" })
  await expect(banner).toContainText(accounts.customer.email)
  await expect(banner).toContainText(`signed in as ${accounts.owner.email}`)
  // The banner stays while they look around the customer's area.
  await page.goto("/client/vms")
  await expect(page.getByRole("listitem").filter({ hasText: customerVM.hostname })).toBeVisible()
  await expect(banner).toBeVisible()

  await banner.getByRole("button", { name: "End" }).click()
  await expect(page).toHaveURL(new RegExp(`/users/${customerID}`))
  await expect(page.getByRole("status").filter({ hasText: "You are viewing as" })).toHaveCount(0)
})
