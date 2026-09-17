import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import { bootstrapToken, password, sessionCookie, wrongPassword } from "./application-fixture";
import {
  alphaCount, apiVersion, assetPage, assetSeries, assetsPath, backendID, betaCount, createdID, digest, importsPath,
  manualFile, pageParameters, pagingAlpha, pagingBeta, pagingCookie, pagingUser, utc, validAsset, validExpiry,
} from "./asset-pagination-data";
import type { AssetPage, IntakeBody, IntakeMetadata, IntakeReceipt, PagingAsset, PagingRole } from "./asset-pagination-data";

type Denial = 400 | 401 | 403 | 404 | 413 | 503;
export interface PagingCall {
  method: string; path: string; workspace: string | undefined; query: Record<string, string>;
  body: Record<string, unknown>; status: number | null; response: Record<string, unknown> | null; failure: string | null;
}
export interface PagingControl { call: PagingCall | null; requested: Promise<void>; delivered: Promise<void>; release: () => void }
interface Gate extends PagingControl { wait: Promise<void>; arrive: () => void; complete: () => void }
interface Reply { status: 200 | 201 | 202 | Denial; gate: Gate }
const secrets = [password, wrongPassword, bootstrapToken, sessionCookie, pagingCookie];
const fields = ["name", "kind", "environment", "criticality", "tags", "ownerId"];

export class AssetPagingAPI {
  authenticated = true;
  readonly roles = new Map<string, PagingRole>([[pagingAlpha.id, "admin"], [pagingBeta.id, "analyst"]]);
  readonly serverRoles = new Map(this.roles);
  readonly assets = new Map<string, PagingAsset>();
  readonly requests: PagingCall[] = [];
  readonly pages: Array<{ call: PagingCall; page: AssetPage }> = [];
  readonly violations: string[] = [];
  readonly pageErrors: string[] = [];
  readonly consoleText: string[] = [];
  readonly imports = new Map<string, { workspace: string; receipt: IntakeReceipt }>();
  externalAttempts = 0;
  private expectedImport: IntakeMetadata | null = null;
  private sessionCompleted = false;
  private replies = new Map<string, Reply[]>();
  private gates = new Set<Gate>();
  private byRequest = new Map<Request, PagingCall>();

  constructor() {
    for (const asset of [...assetSeries(pagingAlpha.id, alphaCount), ...assetSeries(pagingBeta.id, betaCount)]) this.seed(asset);
  }
  seed(asset: PagingAsset) {
    if (!validAsset(asset) || !this.roles.has(asset.workspaceId)) throw new Error("Only valid nonsecret workspace-owned synthetic assets may be seeded.");
    this.assets.set(asset.id, structuredClone(asset));
  }
  calls(method: string, path = assetsPath) { return this.requests.filter((call) => call.method === method && call.path === path); }
  expectImport(input: IntakeMetadata) {
    if (this.assets.get(input.assetId)?.workspaceId !== pagingAlpha.id) throw new Error("Import must select a real synthetic Alpha asset.");
    this.expectedImport = structuredClone(input);
  }
  private queue(key: string, status: Reply["status"], held: boolean) {
    let release!: () => void, arrive!: () => void, complete!: () => void;
    const gate: Gate = {
      call: null, wait: new Promise<void>((resolve) => { release = resolve; }),
      requested: new Promise<void>((resolve) => { arrive = resolve; }),
      delivered: new Promise<void>((resolve) => { complete = resolve; }),
      release: () => release(), arrive: () => arrive(), complete: () => complete(),
    };
    this.replies.set(key, [...(this.replies.get(key) ?? []), { status, gate }]);
    this.gates.add(gate); if (!held) release();
    return gate;
  }
  queuePage(cursor: string, status: 200 | Denial = 200, held = false, workspace = pagingAlpha.id) {
    if (cursor !== "" && !backendID(cursor) || !this.roles.has(workspace)) throw new Error("A page control needs a native cursor and known workspace.");
    return this.queue(`GET ${assetsPath} ${workspace} ${cursor}`, status, held);
  }
  queueCreate(held = false) { return this.queue(`POST ${assetsPath}`, 201, held); }
  queuePatch(id: string, held = false) { return this.queue(`PATCH ${assetsPath}/${id}`, 200, held); }
  queueImport(held = false) { return this.queue(`POST ${importsPath}`, 202, held); }
  async releaseResponses() {
    const arrived = [...this.gates].filter((gate) => gate.call !== null);
    for (const gate of this.gates) gate.release();
    await Promise.all(arrived.map((gate) => gate.delivered));
  }
  private error(status: number) {
    const errors: Record<number, [string, string]> = {
      400: ["invalid-input", "The request is invalid"], 401: ["unauthorized", "Authentication is required"],
      403: ["forbidden", "This operation is not permitted"], 404: ["not-found", "The requested resource was not found"],
      413: ["too-large", "The request exceeds the configured size limit"],
      429: ["rate-limited", "Synthetic combined request budget exceeded"],
      503: ["unavailable", "The operation could not be completed"],
    };
    const [code, message] = errors[status];
    return { apiVersion, error: { code, message, requestId: "synthetic-asset-pagination-request", retryable: false } };
  }
  private async respond(route: Route, call: PagingCall, status: number, response: Record<string, unknown>, reply?: Reply) {
    call.status = status; call.response = structuredClone(response);
    try {
      if (reply) { reply.gate.call = call; reply.gate.arrive(); await reply.gate.wait; }
      if (status === 401) this.authenticated = false;
      await route.fulfill({ status, json: call.response });
    } finally {
      reply?.gate.complete(); if (reply) this.gates.delete(reply.gate);
    }
  }
  async assertPrivate(page: Page, cleared = false) {
    if (page.isClosed()) return;
    const state = await page.evaluate(() => ({
      text: document.body.textContent, cookie: document.cookie,
      storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
      files: [...document.querySelectorAll<HTMLInputElement>('input[type="file"]')].flatMap((input) => [...(input.files ?? [])].map((file) => file.name)),
    }));
    for (const secret of secrets) {
      for (const value of [secret, encodeURIComponent(secret)]) {
        expect(JSON.stringify(state) + page.url() + this.consoleText.join("\n"), "No credentials in DOM, readable cookies, URLs, console or browser storage.").not.toContain(value);
      }
    }
    for (const [key, value] of state.storage) {
      expect(key, "Asset pages, selected IDs and source report bodies are not browser-storage preferences.").toBe("aspm.theme");
      expect(["light", "dark"]).toContain(value);
    }
    if (cleared) expect(state.files).toEqual([]);
  }
  async install(page: Page, origin: string) {
    const local = new URL(origin);
    if (local.protocol !== "http:" || local.hostname !== "127.0.0.1") throw new Error("Only existing loopback browser fixtures are authorized.");
    await page.context().addCookies([{ name: "aspm_session", value: pagingCookie, url: origin, httpOnly: true, sameSite: "Lax" }]);
    page.on("pageerror", (error) => this.pageErrors.push(error.message));
    page.on("console", (message) => this.consoleText.push(message.text()));
    page.on("requestfailed", (request) => {
      const call = this.byRequest.get(request); if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) {
        this.externalAttempts++; this.violations.push("No provider, scanner, source-report URL, cloud or external traffic is authorized.");
        await route.abort(); return;
      }
      if (!url.pathname.startsWith("/api/")) { await route.continue(); return; }
      const headers = await request.allHeaders(), method = request.method(), path = url.pathname;
      const call: PagingCall = {
        method, path, workspace: headers["x-aspm-workspace-id"], query: Object.fromEntries(url.searchParams),
        body: {}, status: null, response: null, failure: null,
      };
      this.requests.push(call); this.byRequest.set(request, call);
      if (this.requests.length > 60) {
        this.violations.push("The unchanged combined 60-API-request per-case budget was exceeded.");
        await this.respond(route, call, 429, this.error(429)); return;
      }
      if (headers.authorization || headers["x-aspm-bootstrap-token"] ||
        secrets.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret)))) {
        this.violations.push("Use only the HttpOnly application session, never provider/bootstrap/bearer/URL credentials.");
      }
      const id = new RegExp(`^${assetsPath}/([a-f0-9]{32})$`).exec(path)?.[1];
      const write = method === "POST" && [assetsPath, importsPath, "/api/v1/login", "/api/v1/logout"].includes(path) ||
        method === "PATCH" && id !== undefined;
      if (method !== "GET" && !write || url.search !== "" && !(method === "GET" && path === assetsPath)) {
        this.violations.push(`Undeclared asset operation: ${method} ${path}. No delete, bulk, search, offset or page-number API.`);
        await route.abort(); return;
      }
      if (write) {
        if (headers.origin !== origin) this.violations.push("Every write needs the matching Origin.");
        if (path !== "/api/v1/logout") {
          let parsed: unknown; try { parsed = request.postDataJSON(); } catch { parsed = null; }
          const type = headers["content-type"]?.split(";").map((part) => part.trim().toLowerCase());
          if (type?.[0] !== "application/json" || type.some((part) => part.startsWith("charset=") && part !== "charset=utf-8") ||
            headers["content-encoding"] && headers["content-encoding"] !== "identity" ||
            parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
            this.violations.push("Writes require the existing unencoded UTF-8 JSON object contract.");
            await this.respond(route, call, 400, this.error(400)); return;
          }
          call.body = parsed as Record<string, unknown>;
          if ((request.postDataBuffer()?.length ?? 0) > (path === importsPath ? 8 << 20 : 64 << 10)) {
            this.violations.push("The native asset 64 KiB / default import 8 MiB JSON bound was exceeded.");
            await this.respond(route, call, 413, this.error(413)); return;
          }
        }
      }
      const cookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${pagingCookie}`);
      const session = () => ({
        apiVersion, user: pagingUser, expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        workspaces: [pagingAlpha, pagingBeta].map((workspace) => ({ ...workspace, role: this.roles.get(workspace.id)! })),
      });
      if (method === "GET" && path === "/api/v1/session") {
        if (this.authenticated && cookie) { await this.respond(route, call, 200, session()); this.sessionCompleted = true; }
        else await this.respond(route, call, 401, this.error(401));
        return;
      }
      if (method === "POST" && path === "/api/v1/login") {
        if (Object.keys(call.body).sort().join(",") !== "email,password" || call.body.email !== pagingUser.email || call.body.password !== password) {
          await this.respond(route, call, 401, this.error(401));
        } else {
          this.authenticated = true; call.status = 200; call.response = session();
          await route.fulfill({ json: call.response, headers: { "set-cookie": `aspm_session=${pagingCookie}; Path=/; HttpOnly; SameSite=Lax` } });
          this.sessionCompleted = true;
        }
        return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected pagination began before the authenticated session completed.");
      if (!this.authenticated || !cookie) { await this.respond(route, call, 401, this.error(401)); return; }
      if (method === "POST" && path === "/api/v1/logout") {
        this.authenticated = false; this.sessionCompleted = false; call.status = 204;
        await route.fulfill({ status: 204, headers: { "set-cookie": "aspm_session=; Max-Age=0; Path=/; HttpOnly; SameSite=Lax" } }); return;
      }
      const workspace = call.workspace;
      if (!workspace || !this.roles.has(workspace)) {
        this.violations.push("A protected request omitted or invented the selected workspace.");
        await this.respond(route, call, 403, this.error(403)); return;
      }
      let cursor = "";
      if (method === "GET" && path === assetsPath) {
        try { cursor = pageParameters(url).cursor; } catch (error) {
          this.violations.push(String(error)); await this.respond(route, call, 400, this.error(400)); return;
        }
      }
      const key = method === "GET" && path === assetsPath ? `GET ${assetsPath} ${workspace} ${cursor}` : `${method} ${path}`;
      const reply = this.replies.get(key)?.shift();
      if (!this.serverRoles.has(workspace)) { await this.respond(route, call, 403, this.error(403), reply); return; }
      if (method === "GET" && path === assetsPath) {
        const snapshot = assetPage(this.assets.values(), workspace, url), status = reply?.status ?? 200;
        if (status === 200) this.pages.push({ call, page: structuredClone(snapshot) });
        await this.respond(route, call, status, status === 200 ? { ...snapshot } : this.error(status), reply);
      } else if (method === "GET" && id) {
        const asset = this.assets.get(id);
        await this.respond(route, call, asset?.workspaceId === workspace ? 200 : 404,
          asset?.workspaceId === workspace ? { apiVersion, asset } : this.error(404), reply);
      } else if (write && (path === assetsPath || id)) {
        if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) { await this.respond(route, call, 403, this.error(403), reply); return; }
        const prior = id ? this.assets.get(id) : null;
        if (id && prior?.workspaceId !== workspace) { await this.respond(route, call, 404, this.error(404), reply); return; }
        if (Object.keys(call.body).some((field) => !fields.includes(field))) {
          this.violations.push("Asset writes accept only intended asset fields, never client IDs, page/revision state or authority.");
          await this.respond(route, call, 400, this.error(400), reply); return;
        }
        const asset = {
          ...(prior ?? { id: createdID, workspaceId: workspace, criticality: "medium", tags: [], ownerId: null }),
          ...call.body,
        } as PagingAsset;
        if (!validAsset(asset)) { await this.respond(route, call, 400, this.error(400), reply); return; }
        this.seed(asset);
        await this.respond(route, call, id ? 200 : 201, { apiVersion, asset }, reply);
      } else if (method === "POST" && path === importsPath) {
        const body = call.body, expected = this.expectedImport;
        if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) { await this.respond(route, call, 403, this.error(403), reply); return; }
        if (!expected || this.assets.get(expected.assetId)?.workspaceId !== workspace ||
          Object.keys(body).sort().join(",") !== [...Object.keys(expected), "apiVersion", "format", "report", "collectedAt"].sort().join(",") ||
          body.apiVersion !== apiVersion || body.format !== "manual" || typeof body.report !== "string" ||
          !Buffer.from(body.report).equals(manualFile.buffer) || !validExpiry(body.collectedAt) ||
          Object.entries(expected).some(([key, value]) => !isDeepStrictEqual(body[key], value))) {
          this.violations.push("Import must use the deliberately selected server asset ID and exact existing manual-report wrapper.");
          await this.respond(route, call, 400, this.error(400), reply); return;
        }
        const input = body as unknown as IntakeBody;
        const receipt: IntakeReceipt = {
          ...expected, id: "e5000000000000000000000000000001", runId: "e6000000000000000000000000000001",
          state: "queued", format: "manual", collectedAt: utc(input.collectedAt), importedAt: "2026-09-17T03:04:05.123456Z",
          reportDigest: digest(manualFile.buffer), observationCount: 0, failure: null,
        };
        this.imports.set(receipt.id, { workspace, receipt });
        await this.respond(route, call, 202, { apiVersion, import: receipt }, reply);
      } else {
        this.violations.push(`Undeclared asset-paging endpoint: ${method} ${path}.`);
        await this.respond(route, call, 404, this.error(404), reply);
      }
    });
  }
}

export const test = base.extend<{ paging: AssetPagingAPI }>({
  paging: async ({ page, baseURL }, use, testInfo) => {
    if (!baseURL) throw new Error("Asset paging requires the unchanged loopback test configuration.");
    const paging = new AssetPagingAPI(); await paging.install(page, new URL(baseURL).origin);
    try { await use(paging); } finally {
      await paging.releaseResponses();
      expect.soft(paging.violations, "Only bounded native asset cursor reads and declared writes, with unchanged auth/Origin/credential limits.").toEqual([]);
      expect.soft(paging.pageErrors).toEqual([]);
      await paging.assertPrivate(page);
      await testInfo.attach("asset-pagination-http-ledger", {
        body: JSON.stringify({
          requests: paging.requests.map((call) => ({
            ...call, body: call.path === "/api/v1/login" ? { credentials: "redacted synthetic sign-in" } :
              typeof call.body.report === "string" ? { ...call.body, report: { bytes: Buffer.byteLength(call.body.report), digest: digest(call.body.report) } } : call.body,
          })),
          violations: paging.violations, pageErrors: paging.pageErrors, externalAttempts: paging.externalAttempts,
          boundary: "Synthetic same-origin HTTP data only; no backend, deletion, native provider or React state replacement.",
        }, null, 2), contentType: "application/json",
      });
    }
  },
});
export { expect } from "@playwright/test";
