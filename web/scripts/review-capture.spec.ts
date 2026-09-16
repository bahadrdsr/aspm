import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "../tests/network";
import { workItems } from "../tests/fixtures";
import type { Page } from "@playwright/test";

const root = fileURLToPath(new URL("..", import.meta.url));
const directory = join(root, ".artifacts", "review");
mkdirSync(directory, { recursive: true });
const captured: { path: string; sha256: string }[] = [];

async function capture(page: Page, path: string): Promise<void> {
  const file = join(directory, path);
  await page.screenshot({ path: file, fullPage: true, animations: "disabled" });
  captured.push({ path, sha256: createHash("sha256").update(readFileSync(file)).digest("hex") });
}

for (const theme of ["light", "dark"] as const) {
  test(`capture the real ${theme} work and gallery views`, async ({ page, api }) => {
    expect(api.requests).toEqual([]);
    await page.emulateMedia({ colorScheme: theme, reducedMotion: "no-preference" });
    await page.goto("/");
    await expect(page.getByRole("table", { name: "Findings" })).toBeVisible();
    await capture(page, `work-${theme}.png`);
    await page.getByRole("button", { name: workItems[0].title, exact: true }).click();
    const dialog = page.getByRole("dialog", { name: workItems[0].title });
    await expect(dialog.getByText("Original evidence", { exact: true })).toBeVisible();
    await capture(page, `finding-${theme}.png`);
    await page.keyboard.press("Escape");
    await page.getByRole("navigation", { name: "Primary" }).getByRole("link", { name: "Settings" }).click();
    await page.getByRole("link", { name: "Component gallery", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Synthetic component gallery" })).toBeVisible();
    await capture(page, `gallery-${theme}.png`);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.emulateMedia({ reducedMotion: "reduce" });
    await capture(page, `gallery-${theme}-mobile-reduced.png`);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
    expect(overflow, "Small-screen gallery must not overflow the viewport.").toBe(false);
  });
}

test.afterAll(() => {
  writeFileSync(join(directory, "capture-manifest.json"), `${JSON.stringify({
    scope: "Actual M01 React/Vite DOM with independent synthetic HTTP fixtures",
    dataOrigin: "synthetic", backendMocked: "HTTP only", componentsMocked: false,
    approvedVisualBaseline: false, liveVerification: false,
    captureStatus: captured.length === 8 ? "complete" : "incomplete",
    capturedAt: new Date().toISOString(),
    desktop: { width: 1366, height: 768 }, mobile: { width: 390, height: 844 },
    screenshotAnimationPolicy: "Settled stills; full/reduced-motion behavior is exercised separately by the unchanged acceptance suite.",
    files: captured,
  }, null, 2)}\n`);
});
