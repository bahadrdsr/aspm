import { fileURLToPath } from "node:url";
import { join } from "node:path";
import { defineConfig } from "@playwright/test";
import acceptance from "../../../playwright.config";

const run = process.env.ASPM_WORK_SEARCH_RUN ?? "red-01";
if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(run)) throw new Error("Use a fresh bounded search run label.");
if (process.env.ASPM_WEB_TEST_PORT !== "18825" || process.env.NODE_TLS_REJECT_UNAUTHORIZED === "0" ||
  acceptance.timeout !== 20_000 || acceptance.expect?.timeout !== 5_000 || acceptance.workers !== 1 ||
  acceptance.retries !== 0 || acceptance.fullyParallel !== false || acceptance.use?.serviceWorkers !== "block" ||
  acceptance.use?.trace !== "retain-on-failure" || acceptance.use?.screenshot !== "only-on-failure" ||
  acceptance.use?.ignoreHTTPSErrors === true) throw new Error("Keep the original browser guards, port 18825 and normal TLS.");
const output = join(fileURLToPath(new URL("../../../../", import.meta.url)), ".artifacts", "work-search-v1", run);
export default defineConfig({
  ...acceptance,
  testDir: fileURLToPath(new URL("../../", import.meta.url)),
  testMatch: "work-search-ui.spec.ts",
  outputDir: join(output, "results"),
  reporter: [["list"], ["json", { outputFile: join(output, "results.json") }]],
});
