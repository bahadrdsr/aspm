import assert from "node:assert/strict";
import { createHash, X509Certificate } from "node:crypto";
import { readFileSync, mkdirSync } from "node:fs";
import https from "node:https";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const web = dirname(dirname(fileURLToPath(import.meta.url)));
const runtime = process.env.ASPM_TEST_RUNTIME;
assert.ok(runtime, "Supply the private local test-runtime directory explicitly.");
const origin = "https://127.0.0.1:18443";
const root = resolve(runtime);
const certificate = new X509Certificate(readFileSync(join(root, "server-cert.pem")));
const pin = createHash("sha256").update(certificate.publicKey.export({ type: "spki", format: "der" })).digest("base64");
await new Promise((accept, reject) => {
  const request = https.get(`${origin}/readyz`, { ca: readFileSync(join(root, "development-ca.pem")), timeout: 5000 }, (response) => {
    response.resume();
    response.on("end", () => response.statusCode === 200 ? accept() : reject(new Error("Local core is not ready.")));
  });
  request.on("timeout", () => request.destroy(new Error("Local readiness timed out.")));
  request.on("error", reject);
});
process.env.PLAYWRIGHT_BROWSERS_PATH = join(web, ".cache", "playwright");
const { chromium } = await import("@playwright/test");
const account = JSON.parse(readFileSync(join(root, "development-account.json"), "utf8"));
const browser = await chromium.launch({ headless: true, args: [`--ignore-certificate-errors-spki-list=${pin}`] });
try {
  const context = await browser.newContext({ viewport: { width: 1366, height: 900 }, reducedMotion: "reduce" });
  await context.route("**/*", async (route) => {
    if (new URL(route.request().url()).origin !== origin) {
      await route.abort();
      throw new Error("Live fixture attempted an external request.");
    }
    await route.continue();
  });
  const page = await context.newPage();
  await page.goto(`${origin}/#/work`);
  const form = page.getByRole("form", { name: "Sign in", exact: true });
  await form.waitFor();
  assert.equal(await page.getByRole("table", { includeHidden: true }).count(), 0);
  await form.getByLabel("Email", { exact: true }).fill(account.email);
  await form.getByLabel("Password", { exact: true }).fill(account.password);
  await form.getByRole("button", { name: "Sign in", exact: true }).click();
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  await workspace.waitFor();
  await workspace.selectOption({ label: "Local validation" });
  await page.getByRole("table", { name: "Findings" }).waitFor();
  const text = await page.locator("body").innerText();
  assert.ok(text.includes("Synthetic validation finding - not a real vulnerability"));
  assert.ok(!text.includes(account.password));
  const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookies: document.cookie }));
  assert.ok(!storage.includes(account.password) && !storage.includes("aspm_session"));
  const directory = join(web, ".artifacts", "security-ui-live");
  mkdirSync(directory, { recursive: true });
  await page.screenshot({ path: join(directory, "authenticated.png"), fullPage: true });
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await form.waitFor();
  assert.equal(await page.getByRole("table", { includeHidden: true }).count(), 0);
  assert.equal(await page.getByRole("combobox", { name: "Workspace", includeHidden: true }).count(), 0);
  assert.equal((await context.cookies()).filter((cookie) => cookie.name === "aspm_session").length, 0);
  await page.reload();
  await form.waitFor();
  assert.equal(await page.getByRole("table", { includeHidden: true }).count(), 0);
  await page.screenshot({ path: join(directory, "signed-out.png"), fullPage: true });
  console.log("PASS: real HTTPS session entry, protected finding display, no browser credential storage, logout revocation and reload denial. No HTTP responses mocked.");
} finally {
  await browser.close();
}
