import { test, expect, type Page } from "@playwright/test"
import { customerVM, stateFile } from "./fixtures"

test.use({ storageState: stateFile("customer") })

const header = (page: Page) => page.getByRole("main")
const state = (page: Page, word: string) => header(page).getByText(word, { exact: true }).first()

test("a customer sees their VM and powers it off and on", async ({ page }) => {
  await page.goto("/client/vms")
  const row = page.getByRole("listitem").filter({ hasText: customerVM.hostname })
  await expect(row).toContainText("Running")
  await row.getByRole("link", { name: new RegExp(customerVM.hostname) }).click()

  await expect(page.getByRole("heading", { level: 1, name: customerVM.hostname })).toBeVisible()
  await expect(state(page, "Running")).toBeVisible()

  await page.getByRole("button", { name: "Stop", exact: true }).click()
  const confirm = page.getByRole("dialog", { name: `Stop ${customerVM.hostname}?` })
  await confirm.getByRole("button", { name: "Stop", exact: true }).click()
  await expect(state(page, "Stopped")).toBeVisible()
  await expect(page.getByRole("button", { name: "Start", exact: true })).toBeVisible()

  await page.getByRole("button", { name: "Start", exact: true }).click()
  await expect(state(page, "Running")).toBeVisible()
  await expect(page.getByRole("button", { name: "Stop", exact: true })).toBeVisible()
})

test("a customer adds a firewall rule", async ({ page }) => {
  const port = String(20000 + (Date.now() % 10000))
  await page.goto(`/client/vms/${customerVM.id}`)
  await page.getByRole("tab", { name: "Firewall" }).click()
  const panel = page.getByRole("tabpanel", { name: "Firewall" })

  await panel.getByRole("button", { name: "Add Rule" }).click()
  const dialog = page.getByRole("dialog", { name: "Add Firewall Rule" })
  await dialog.getByPlaceholder("80 or 8000-8100").fill(port)
  await dialog.getByRole("button", { name: "Add Rule" }).click()
  await expect(dialog).toBeHidden()

  const rule = panel.getByRole("row").filter({ hasText: port })
  await expect(rule).toContainText("tcp")
  await expect(rule).toContainText("inbound")
  // Kept after a reload: it was saved, not only drawn.
  await page.reload()
  await page.getByRole("tab", { name: "Firewall" }).click()
  await expect(page.getByRole("tabpanel", { name: "Firewall" }).getByRole("row").filter({ hasText: port })).toBeVisible()
})

test("a customer takes a backup", async ({ page }) => {
  await page.goto(`/client/vms/${customerVM.id}`)
  await page.getByRole("tab", { name: "Backups" }).click()
  const panel = page.getByRole("tabpanel", { name: "Backups" })
  await panel.getByRole("button", { name: "Create Backup" }).click()
  // The job runs on the node and the row follows it to the end.
  await expect(panel.getByText("Completed", { exact: true }).first()).toBeVisible({ timeout: 45_000 })
  await expect(panel.getByRole("button", { name: "Restore" }).first()).toBeVisible()
})
