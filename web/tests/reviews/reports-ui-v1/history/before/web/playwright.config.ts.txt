import { fileURLToPath } from "node:url";
import { defineConfig } from "@playwright/test";

const root = fileURLToPath(new URL(".", import.meta.url));
const port = Number(process.env.ASPM_WEB_TEST_PORT ?? "48217");
if (!Number.isInteger(port) || port < 1024 || port > 65535) {
  throw new Error("ASPM_WEB_TEST_PORT must be an integer between 1024 and 65535.");
}
const baseURL = `http://127.0.0.1:${port}`;
const vite = fileURLToPath(new URL("./node_modules/vite/bin/vite.js", import.meta.url));

export default defineConfig({
  testDir: "./tests",
  testMatch: "**/*.spec.ts",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 20_000,
  expect: { timeout: 5_000 },
  outputDir: ".artifacts/test-results",
  reporter: [
    ["list"],
    ["json", { outputFile: ".artifacts/reports/m01-results.json" }],
  ],
  use: {
    baseURL,
    browserName: "chromium",
    headless: true,
    viewport: { width: 1366, height: 768 },
    colorScheme: "light",
    reducedMotion: "no-preference",
    serviceWorkers: "block",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command: `"${process.execPath}" "${vite}" --host 127.0.0.1 --port ${port} --strictPort`,
    cwd: root,
    url: `${baseURL}/tests/harness/index.html`,
    timeout: 60_000,
    reuseExistingServer: false,
    gracefulShutdown: { signal: "SIGTERM", timeout: 5_000 },
    stdout: "ignore",
    stderr: "pipe",
  },
});
