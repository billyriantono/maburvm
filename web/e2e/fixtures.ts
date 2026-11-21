import { expect, type Page } from "@playwright/test"

// The accounts and the VM seed.sql creates. Test-only credentials for a
// throwaway database.
export const accounts = {
  owner: { email: "owner@e2e.test", password: "E2e-Admin-Pass-1!" },
  customer: { email: "customer@e2e.test", password: "E2e-Client-Pass-1!" },
  support: { email: "support@e2e.test", password: "E2e-Support-Pass-1!" },
  locked: { email: "locked@e2e.test", password: "E2e-Client-Pass-1!" },
} as const

export type Role = "owner" | "customer" | "support"

export const customerID = "e2e00000-0000-0000-0000-000000000002"
export const customerVM = { id: "e2e00000-0000-0000-0000-0000000000e1", hostname: "shop-e2e" }

// Where each role's signed-in browser state is kept between the setup and
// the tests: one sign-in per role, because the panel rate-limits sign-ins.
export const stateFile = (role: Role) => `e2e/.auth/${role}.json`

export async function submitSignIn(page: Page, email: string, password: string) {
  await page.goto("/login")
  await page.getByLabel("Email address").fill(email)
  await page.getByLabel("Password", { exact: true }).fill(password)
  const button = page.getByRole("button", { name: "Sign in" })
  await expect(button).toBeEnabled()
  await button.click()
}
