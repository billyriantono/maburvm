import { test, expect, type Page } from "@playwright/test"
import { accounts } from "./fixtures"

// tryPassword submits the sign-in form and waits for the panel's answer.
async function tryPassword(page: Page, password: string) {
  await page.getByLabel("Password", { exact: true }).fill(password)
  const answer = page.waitForResponse((r) => r.url().endsWith("/api/v1/auth/login"))
  await page.getByRole("button", { name: "Sign in" }).click()
  return (await answer).status()
}

// The form's message; Next.js keeps an empty alert region of its own on the page.
const message = (page: Page, text: RegExp) => page.getByRole("alert").filter({ hasText: text })

async function openSignIn(page: Page, email: string) {
  await page.goto("/login")
  await page.getByLabel("Email address").fill(email)
  await expect(page.getByRole("button", { name: "Sign in" })).toBeEnabled()
}

test("a wrong password is refused and the right one signs in", async ({ page }) => {
  await openSignIn(page, accounts.owner.email)
  expect(await tryPassword(page, "not-the-password")).toBe(401)
  await expect(message(page, /invalid email or password/i)).toBeVisible()
  await expect(page).toHaveURL(/\/login/)

  await tryPassword(page, accounts.owner.password)
  await expect(page).toHaveURL(/\/dashboard/)
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible()
})

// seed.sql locks an account after three wrong passwords.
test("repeated wrong passwords lock the account, and say so", async ({ page }) => {
  await openSignIn(page, accounts.locked.email)
  for (const password of ["wrong-1", "wrong-2", "wrong-3"]) {
    await tryPassword(page, password)
  }

  // Locked: even the right password is refused, with the reason and the way out.
  expect(await tryPassword(page, accounts.locked.password)).toBe(429)
  const alert = message(page, /too many failed sign-in attempts/i)
  await expect(alert).toBeVisible()
  await expect(alert).toContainText(/reset your password/i)
  await expect(page).toHaveURL(/\/login/)
})
