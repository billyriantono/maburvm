import { defineConfig, devices } from "@playwright/test"

// Browser tests against a local panel, the fake node agent and a throwaway
// database, all started by e2e/stack.sh. See e2e/README.md.
const port = process.env.E2E_WEB_PORT ?? "3480"

export default defineConfig({
  testDir: "./e2e",
  // The tests share one panel and one customer VM, so they run one at a time.
  workers: 1,
  fullyParallel: false,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  retries: process.env.CI ? 1 : 0,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [["list"], ["github"]] : "list",
  use: {
    ...devices["Desktop Chrome"],
    // E2E_BROWSER_CHANNEL=chrome uses the installed Chrome instead of Playwright's own.
    channel: process.env.E2E_BROWSER_CHANNEL || undefined,
    baseURL: `http://127.0.0.1:${port}`,
    trace: "retain-on-failure",
  },
  projects: [
    { name: "setup", testMatch: /auth\.setup\.ts/ },
    { name: "flows", testMatch: /.*\.e2e\.ts/, dependencies: ["setup"] },
  ],
  webServer: {
    command: "bash e2e/stack.sh",
    url: `http://127.0.0.1:${port}/login`,
    timeout: 300_000,
    reuseExistingServer: !process.env.CI,
    stdout: "pipe",
    // Time for stack.sh to stop what it started and drop its database.
    gracefulShutdown: { signal: "SIGTERM", timeout: 15_000 },
  },
})
