import { test, expect } from "@playwright/test"
import { stateFile } from "./fixtures"

test.use({ storageState: stateFile("owner") })

test("a saved setting is kept and shown to everyone", async ({ page, browser }) => {
  const tagline = `Servers tested ${Date.now().toString(36)}`
  await page.goto("/settings/branding")
  await page.getByLabel("Tagline").fill(tagline)
  await page.getByRole("button", { name: "Save and publish" }).click()
  await expect(page.getByText(/Branding saved/)).toBeVisible()

  await page.reload()
  await expect(page.getByLabel("Tagline")).toHaveValue(tagline)

  // The sign-in page, for someone not signed in, carries it.
  const visitor = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const signIn = await visitor.newPage()
  await signIn.goto("/login")
  await expect(signIn.getByText(tagline)).toBeVisible()
  await visitor.close()
})
