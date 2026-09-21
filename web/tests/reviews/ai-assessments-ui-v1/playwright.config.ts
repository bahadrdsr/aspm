import { fileURLToPath } from "node:url";
import { join } from "node:path";
import { defineConfig } from "@playwright/test";
import acceptance from "../../../playwright.config";

const run = process.env.ASPM_AI_ASSESS_UI_RUN ?? "red-01";
if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(run)) throw new Error("Use a fresh bounded author run label.");
if (acceptance.timeout !== 20_000 || acceptance.expect?.timeout !== 5_000 ||
  acceptance.workers !== 1 || acceptance.retries !== 0 || acceptance.fullyParallel !== false ||
  acceptance.use?.trace !== "retain-on-failure" || acceptance.use?.screenshot !== "only-on-failure" ||
  acceptance.use?.serviceWorkers !== "block") throw new Error("Original browser guard settings changed.");
const root = fileURLToPath(new URL("../../../../", import.meta.url));
const output = join(root, ".artifacts", "ai-assessments-ui-v1-author", run);

export default defineConfig({
  ...acceptance,
  testDir: fileURLToPath(new URL("../../", import.meta.url)),
  outputDir: join(output, "results"),
  reporter: [["list"], ["json", { outputFile: join(output, "results.json") }]],
});
