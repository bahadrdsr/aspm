import { fileURLToPath } from "node:url";
import { join } from "node:path";
import { defineConfig } from "@playwright/test";
import acceptance from "../../../playwright.config";

const run = process.env.ASPM_TEAMS_UI_RUN ?? "red-01";
if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(run)) throw new Error("Use a bounded evidence run label.");
if (acceptance.timeout !== 20_000 || acceptance.expect?.timeout !== 5_000 ||
  acceptance.workers !== 1 || acceptance.retries !== 0 || acceptance.fullyParallel !== false ||
  acceptance.use?.trace !== "retain-on-failure" || acceptance.use?.screenshot !== "only-on-failure" ||
  acceptance.use?.serviceWorkers !== "block" || acceptance.use?.ignoreHTTPSErrors === true ||
  process.env.NODE_TLS_REJECT_UNAUTHORIZED === "0") throw new Error("Frozen browser/TLS budgets changed.");
const root = fileURLToPath(new URL("../../../", import.meta.url));
const output = join(root, ".artifacts", "teams-workflows-ui-v1-author", run);
const port = 18828;
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  ...acceptance,
  testDir: fileURLToPath(new URL("../../", import.meta.url)),
  testMatch: "teams-workflows-ui.spec.ts",
  outputDir: join(output, "results"),
  reporter: [["list"], ["json", { outputFile: join(output, "results.json") }]],
  use: { ...acceptance.use, baseURL, ignoreHTTPSErrors: false },
  webServer: {
    command: `"${process.execPath}" "${join(root, "node_modules", "vite", "bin", "vite.js")}" --host 127.0.0.1 --port ${port} --strictPort`,
    cwd: root, url: `${baseURL}/tests/harness/index.html`, timeout: 60_000, reuseExistingServer: false,
    gracefulShutdown: { signal: "SIGTERM", timeout: 5_000 }, stdout: "ignore", stderr: "pipe",
  },
});
