import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, test as base } from "@playwright/test";
import type { Page } from "@playwright/test";
import { apiError, catalogResponse, findingResponse, syntheticSession, workResponse } from "./fixtures";

export function requireProductionUI(): void {
  for (const file of ["src/App.tsx", "src/main.tsx", "index.html"]) {
    const path = fileURLToPath(new URL(`../${file}`, import.meta.url));
    expect(existsSync(path), `M01 production UI is intentionally missing: web/${file}. The coder must implement it.`).toBe(true);
  }
}

export class MockAPI {
  readonly violations: string[] = [];
  readonly requests: Array<{ method: string; path: string }> = [];
  workState: "populated" | "empty" | 403 | 503 = "populated";
  private gate: Promise<void> | null = null;
  private release: (() => void) | null = null;

  holdWork(): void {
    this.gate = new Promise<void>((resolve) => { this.release = resolve; });
  }

  releaseWork(): void {
    this.release?.();
    this.gate = null;
    this.release = null;
  }

  async install(page: Page, origin: string): Promise<void> {
    await page.route("**/*", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== origin) {
        this.violations.push(`Unexpected external request to ${url.origin}.`);
        await route.abort();
        return;
      }
      if (!url.pathname.startsWith("/api/")) {
        await route.continue();
        return;
      }
      this.requests.push({ method: request.method(), path: url.pathname });
      if (request.method() !== "GET" || request.headers().authorization) {
        this.violations.push("The M01 shell made an unapproved write or sent a bearer/provider credential.");
        await route.abort();
        return;
      }
      if (url.pathname === "/api/v1/session") {
        await route.fulfill({ json: syntheticSession() });
        return;
      }
      if (url.pathname === "/api/v1/assets") {
        await route.fulfill({ json: { apiVersion: "aspm/v1alpha1", items: [], total: 0, nextCursor: null } });
        return;
      }
      if (url.pathname === "/api/v1/work") {
        await this.gate;
        if (typeof this.workState === "number") {
          await route.fulfill({ status: this.workState, json: apiError(this.workState) });
        } else {
          await route.fulfill({
            json: workResponse(url.searchParams.get("q") ?? "", this.workState === "empty" ? [] : undefined),
          });
        }
        return;
      }
      if (url.pathname === `/api/v1/findings/${findingResponse.finding.id}`) {
        await route.fulfill({ json: findingResponse });
        return;
      }
      if (url.pathname === "/api/v1/integrations/catalog") {
        await route.fulfill({ json: catalogResponse });
        return;
      }
      this.violations.push(`Undeclared M01 API endpoint: ${url.pathname}.`);
      await route.fulfill({ status: 404, json: apiError(404) });
    });
  }
}

export const test = base.extend<{ api: MockAPI }>({
  api: async ({ page, baseURL }, use) => {
    if (!baseURL) throw new Error("The M01 test server has no baseURL.");
    const api = new MockAPI();
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => { pageErrors.push(error.message); });
    await api.install(page, new URL(baseURL).origin);
    try {
      await use(api);
    } finally {
      api.releaseWork();
      expect.soft(api.violations, "Only the declared read-only HTTP boundary may be mocked or called.").toEqual([]);
      expect.soft(pageErrors, "The real UI must not raise unhandled browser exceptions.").toEqual([]);
      if (!page.isClosed() && page.url().startsWith(baseURL)) {
        const keys = await page.evaluate(() => [...Object.keys(localStorage), ...Object.keys(sessionStorage)]);
        expect.soft(keys.filter((key) => /token|api.?key|password|secret|credential/i.test(key)),
          "Long-lived credentials must not be placed in browser storage.").toEqual([]);
      }
    }
  },
});

export { expect } from "@playwright/test";
