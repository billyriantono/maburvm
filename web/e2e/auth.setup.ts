import { test as setup, expect } from "@playwright/test"
import { accounts, stateFile, submitSignIn, type Role } from "./fixtures"

for (const role of ["owner", "customer", "support"] as Role[]) {
  setup(`sign in as ${role}`, async ({ page }) => {
    await submitSignIn(page, accounts[role].email, accounts[role].password)
    await expect(page).toHaveURL(/\/(client\/)?dashboard/)
    await page.context().storageState({ path: stateFile(role) })
  })
}
