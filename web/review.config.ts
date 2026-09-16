import { defineConfig } from "@playwright/test";
import acceptance from "./playwright.config";

export default defineConfig({
  ...acceptance,
  testDir: "./scripts",
  testMatch: "review-capture.spec.ts",
  outputDir: ".artifacts/review-run",
  reporter: [["list"], ["json", { outputFile: ".artifacts/reports/m01-visual-capture.json" }]],
});
