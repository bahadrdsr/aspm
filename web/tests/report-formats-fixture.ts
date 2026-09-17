import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import { bootstrapToken, password, sessionCookie, wrongPassword } from "./application-fixture";
import {
  acceptedAt, apiVersion, assetsPath, backendID, betaAsset, digest, exactKeys, firstAsset, importsPath,
  intakeAlpha, intakeBeta, intakeCookie, intakeUser, literalMapping, profiles, selectedAsset,
  uploadLimit, utc, validExpiry, validText,
} from "./report-formats-data";
import type { IntakeBody, IntakeMetadata, IntakeReceipt, IntakeRole, IntakeState, ReportFile } from "./report-formats-data";

type Denial = 400 | 401 | 403 | 404 | 413 | 415 | 503;
export interface IntakeCall {
  method: string; path: string; workspace: string | undefined; query: string; body: Record<string, unknown>;
  bodyBytes: number; status: number | null; response: Record<string, unknown> | null; failure: string | null;
}
export interface IntakeControl {
  call: IntakeCall | null; requested: Promise<void>; delivered: Promise<void>; release: () => void;
}
interface Gate extends IntakeControl { wait: Promise<void>; arrive: () => void; complete: () => void }
interface Reply { status: 200 | 202 | Denial; gate: Gate }
const secrets = [password, wrongPassword, bootstrapToken, sessionCookie, intakeCookie];

export class ReportFormatsAPI {
  authenticated = true;
  readonly roles = new Map<string, IntakeRole>([[intakeAlpha.id, "analyst"], [intakeBeta.id, "analyst"]]);
  readonly serverRoles = new Map(this.roles);
  readonly assets = [firstAsset, selectedAsset, betaAsset];
  readonly requests: IntakeCall[] = [];
  readonly violations: string[] = [];
  readonly pageErrors: string[] = [];
  readonly consoleText: string[] = [];
  readonly imports = new Map<string, { workspace: string; receipt: IntakeReceipt; body: IntakeBody }>();
  externalAttempts = 0;
  private expected = new Map<string, { file: ReportFile; metadata: IntakeMetadata; workspace: string }>();
  private replies = new Map<string, Reply[]>();
  private gates = new Set<Gate>();
  private byRequest = new Map<Request, IntakeCall>();
  private sessionCompleted = false;

  calls(method: string, path: string) { return this.requests.filter((call) => call.method === method && call.path === path); }
  expectUpload(file: ReportFile, metadata: IntakeMetadata, workspace = intakeAlpha.id) {
    if (!profiles.includes(file.format) || !this.assets.some((asset) => asset.id === metadata.assetId && asset.workspaceId === workspace)) {
      throw new Error("Only explicit synthetic files and selected-workspace assets may be registered.");
    }
    this.expected.set(`${workspace}:${metadata.scanId}`, { file, metadata: structuredClone(metadata), workspace });
  }
  private queue(method: string, path: string, status: Reply["status"], held: boolean) {
    let release!: () => void, arrive!: () => void, complete!: () => void;
    const gate: Gate = {
      call: null, wait: new Promise<void>((resolve) => { release = resolve; }),
      requested: new Promise<void>((resolve) => { arrive = resolve; }),
      delivered: new Promise<void>((resolve) => { complete = resolve; }),
      release: () => release(), arrive: () => arrive(), complete: () => complete(),
    };
    const key = `${method} ${path}`;
    this.replies.set(key, [...(this.replies.get(key) ?? []), { status, gate }]);
    this.gates.add(gate);
    if (!held) release();
    return gate;
  }
  queueUpload(status: 202 | Denial = 202, held = false) { return this.queue("POST", importsPath, status, held); }
  queueStatus(id: string, status: 200 | Denial = 200, held = false) {
    if (!this.imports.has(id)) throw new Error("Read controls require an acknowledged synthetic import.");
    return this.queue("GET", `${importsPath}/${id}`, status, held);
  }
  publishState(id: string, state: IntakeState) {
    const stored = this.imports.get(id);
    if (!stored || ["succeeded", "failed"].includes(stored.receipt.state)) throw new Error("Only nonterminal HTTP response data may advance.");
    stored.receipt = {
      ...stored.receipt, state, observationCount: state === "succeeded" ? 1 : 0,
      failure: state === "failed" ? {
        code: "invalid-report", message: "Report does not match the admitted format or exceeds parser limits", retryable: false,
      } : null,
    };
    return structuredClone(stored.receipt);
  }
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
      415: ["unsupported-format", "The content or report format is not supported"],
      429: ["rate-limited", "Synthetic combined request budget exceeded"],
      503: ["unavailable", "The operation could not be completed"],
    };
    const [code, message] = errors[status];
    return { apiVersion, error: { code, message, requestId: "synthetic-report-formats-request", retryable: false } };
  }
  private async respond(route: Route, call: IntakeCall, status: number, response: Record<string, unknown>, reply?: Reply) {
    call.status = status; call.response = structuredClone(response);
    try {
      if (reply) { reply.gate.call = call; reply.gate.arrive(); await reply.gate.wait; }
      if (status === 401) this.authenticated = false;
      await route.fulfill({ status, json: call.response });
    } finally {
      reply?.gate.complete();
      if (reply) this.gates.delete(reply.gate);
    }
  }
  private validateUpload(call: IntakeCall): IntakeBody | null {
    const body = call.body, generic = body.format === "generic-json" || body.format === "generic-csv";
    const fields = ["apiVersion", "assetId", "format", "report", "sourceId", "scanId", "scope", "sourceScanAt",
      "collectedAt", "sourceStatus", "scanKind", "completeness", ...(generic ? ["mapping"] : [])];
    const scope = body.scope as IntakeMetadata["scope"] | undefined;
    if (!exactKeys(body, fields) || body.apiVersion !== apiVersion || !backendID(body.assetId) ||
      typeof body.format !== "string" || !profiles.some((format) => format === body.format) ||
      ![body.sourceId, body.scanId, scope?.id, scope?.revision, scope?.branch].every((value) => validText(value, 512)) ||
      !scope || !exactKeys(scope, ["id", "revision", "branch"]) || typeof body.report !== "string" ||
      !validExpiry(body.collectedAt) || body.sourceScanAt !== null && !validExpiry(body.sourceScanAt) ||
      !["succeeded", "failed"].includes(String(body.sourceStatus)) || !["full", "delta"].includes(String(body.scanKind)) ||
      !["complete", "partial", "unknown"].includes(String(body.completeness))) return null;
    if (generic && (body.mapping === null || typeof body.mapping !== "object" || Array.isArray(body.mapping) ||
      !exactKeys(body.mapping, Object.keys(literalMapping)) ||
      Object.entries(literalMapping).some(([key, value]) => (body.mapping as Record<string, unknown>)[key] !== value))) return null;
    const expected = this.expected.get(`${call.workspace}:${String(body.scanId)}`);
    if (!expected || expected.file.format !== body.format || !Buffer.from(body.report).equals(expected.file.buffer)) return null;
    for (const [key, value] of Object.entries(expected.metadata)) {
      if (!isDeepStrictEqual(body[key], value)) return null;
    }
    return body as unknown as IntakeBody;
  }
  async assertPrivate(page: Page, cleared = false) {
    if (page.isClosed()) return;
    const state = await page.evaluate(() => ({
      text: document.body.textContent, cookie: document.cookie,
      storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
      files: [...document.querySelectorAll<HTMLInputElement>('input[type="file"]')].flatMap((input) => [...(input.files ?? [])].map((file) => file.name)),
    }));
    for (const secret of secrets) {
      expect(JSON.stringify(state) + page.url() + this.consoleText.join("\n"), "Session/setup credentials never belong in report UI text, storage, URLs or console.").not.toContain(secret);
    }
    for (const [key, value] of state.storage) {
      expect(key, "Report bytes, names, progress and provenance must not be persisted in browser storage.").toBe("aspm.theme");
      expect(["light", "dark"]).toContain(value);
    }
    if (cleared) expect(state.files, "Closing or losing scope clears the real selected file.").toEqual([]);
  }
  async install(page: Page, origin: string) {
    const local = new URL(origin);
    if (local.protocol !== "http:" || local.hostname !== "127.0.0.1") throw new Error("Only the existing loopback HTTP browser boundary is authorized.");
    await page.context().addCookies([{ name: "aspm_session", value: intakeCookie, url: origin, httpOnly: true, sameSite: "Lax" }]);
    page.on("pageerror", (error) => this.pageErrors.push(error.message));
    page.on("console", (message) => this.consoleText.push(message.text()));
    page.on("requestfailed", (request) => {
      const call = this.byRequest.get(request);
      if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) {
        this.externalAttempts += 1;
        this.violations.push("No scanner, report URL, provider, cloud, OAuth or external HTTP request is authorized.");
        await route.abort(); return;
      }
      if (!url.pathname.startsWith("/api/")) { await route.continue(); return; }
      const headers = await request.allHeaders(), method = request.method(), path = url.pathname;
      const call: IntakeCall = {
        method, path, workspace: headers["x-aspm-workspace-id"], query: url.search, body: {},
        bodyBytes: 0, status: null, response: null, failure: null,
      };
      this.requests.push(call); this.byRequest.set(request, call);
      if (this.requests.length > 60) {
        this.violations.push("The unchanged combined 60-API-request per-case budget was exceeded.");
        await this.respond(route, call, 429, this.error(429)); return;
      }
      if (headers.authorization || headers["x-aspm-bootstrap-token"] ||
        secrets.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret)))) {
        this.violations.push("Report intake uses only the HttpOnly application session, never provider/bearer/bootstrap URL authority.");
      }
      const write = method === "POST" && [importsPath, "/api/v1/login", "/api/v1/logout"].includes(path);
      if (method !== "GET" && !write || url.search !== "") {
        this.violations.push(`Undeclared report operation: ${method} ${path}${url.search}.`);
        await route.abort(); return;
      }
      if (write) {
        if (headers.origin !== origin) this.violations.push("Writes require matching Origin.");
        if (path !== "/api/v1/logout") {
          let parsed: unknown;
          try { parsed = request.postDataJSON(); } catch { parsed = null; }
          const contentType = headers["content-type"]?.split(";").map((part) => part.trim().toLowerCase());
          if (contentType?.[0] !== "application/json" ||
            contentType.some((part) => part.startsWith("charset=") && part !== "charset=utf-8") ||
            headers["content-encoding"] && headers["content-encoding"] !== "identity" ||
            parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
            this.violations.push("Intake is one UTF-8 JSON wrapper, not multipart, native form navigation or encoded/compressed data.");
            await this.respond(route, call, 400, this.error(400)); return;
          }
          call.body = parsed as Record<string, unknown>;
          call.bodyBytes = request.postDataBuffer()?.length ?? 0;
          if (call.bodyBytes > uploadLimit) {
            this.violations.push("The complete encoded request exceeded the real default 8 MiB intake limit.");
            await this.respond(route, call, 413, this.error(413)); return;
          }
        }
      }
      const cookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${intakeCookie}`);
      const session = () => ({
        apiVersion, user: intakeUser, expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        workspaces: [intakeAlpha, intakeBeta].map((workspace) => ({ ...workspace, role: this.roles.get(workspace.id)! })),
      });
      if (method === "GET" && path === "/api/v1/session") {
        if (this.authenticated && cookie) { await this.respond(route, call, 200, session()); this.sessionCompleted = true; }
        else await this.respond(route, call, 401, this.error(401));
        return;
      }
      if (method === "POST" && path === "/api/v1/login") {
        if (!exactKeys(call.body, ["email", "password"]) || call.body.email !== intakeUser.email || call.body.password !== password) {
          await this.respond(route, call, 401, this.error(401));
        } else {
          this.authenticated = true; call.status = 200; call.response = session();
          await route.fulfill({ json: call.response, headers: { "set-cookie": `aspm_session=${intakeCookie}; Path=/; HttpOnly; SameSite=Lax` } });
          this.sessionCompleted = true;
        }
        return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected intake began before the authenticated session completed.");
      if (!this.authenticated || !cookie) { await this.respond(route, call, 401, this.error(401)); return; }
      if (method === "POST" && path === "/api/v1/logout") {
        this.authenticated = false; this.sessionCompleted = false; call.status = 204;
        await route.fulfill({ status: 204, headers: { "set-cookie": "aspm_session=; Max-Age=0; Path=/; HttpOnly; SameSite=Lax" } }); return;
      }
      const workspace = call.workspace;
      if (!workspace || !this.roles.has(workspace)) {
        this.violations.push("A protected request omitted or invented the selected session-owned workspace.");
        await this.respond(route, call, 403, this.error(403)); return;
      }
      const reply = this.replies.get(`${method} ${path}`)?.shift();
      if (!this.serverRoles.has(workspace)) { await this.respond(route, call, 403, this.error(403), reply); return; }
      if (method === "GET" && path === assetsPath) {
        const items = this.assets.filter((asset) => asset.workspaceId === workspace);
        await this.respond(route, call, 200, { apiVersion, dataOrigin: "synthetic", items, total: items.length, nextCursor: null });
      } else if (method === "POST" && path === importsPath) {
        const body = this.validateUpload(call);
        if (!body) {
          this.violations.push("Only a registered real-file report with its exact wrapper, literal mapping and explicit provenance may be submitted.");
          await this.respond(route, call, 400, this.error(400), reply); return;
        }
        let status = reply?.status ?? 202;
        if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) status = 403;
        if (status >= 400) { await this.respond(route, call, status, this.error(status), reply); return; }
        const ordinal = (this.imports.size + 1).toString(16).padStart(30, "0");
        const receipt: IntakeReceipt = {
          id: `e3${ordinal}`, runId: `e4${ordinal}`, state: "queued", assetId: body.assetId, format: body.format,
          sourceId: body.sourceId, scanId: body.scanId, scope: body.scope,
          sourceScanAt: body.sourceScanAt === null ? null : utc(body.sourceScanAt), collectedAt: utc(body.collectedAt),
          importedAt: acceptedAt, sourceStatus: body.sourceStatus, scanKind: body.scanKind, completeness: body.completeness,
          reportDigest: digest(Buffer.from(body.report)), observationCount: 0, failure: null,
        };
        this.imports.set(receipt.id, { workspace, receipt: structuredClone(receipt), body: structuredClone(body) });
        await this.respond(route, call, 202, { apiVersion, dataOrigin: "synthetic", import: receipt }, reply);
      } else if (method === "GET" && new RegExp(`^${importsPath}/[a-f0-9]{32}$`).test(path)) {
        const stored = this.imports.get(path.split("/").at(-1)!);
        if (!stored || stored.workspace !== workspace) { await this.respond(route, call, 404, this.error(404), reply); return; }
        const status = reply?.status ?? 200;
        await this.respond(route, call, status, status === 200 ? { apiVersion, dataOrigin: "synthetic", import: stored.receipt } : this.error(status), reply);
      } else {
        this.violations.push(`Undeclared intake endpoint: ${method} ${path}. No parser discovery, import-history, native scanner or execution API exists in this slice.`);
        await this.respond(route, call, 404, this.error(404), reply);
      }
    });
  }
}

export const test = base.extend<{ formats: ReportFormatsAPI }>({
  formats: async ({ page, baseURL }, use, testInfo) => {
    if (!baseURL) throw new Error("Report formats need the unchanged loopback browser configuration.");
    const formats = new ReportFormatsAPI();
    await formats.install(page, new URL(baseURL).origin);
    try { await use(formats); } finally {
      await formats.releaseResponses();
      expect.soft(formats.violations, "Only exact same-origin intake, unchanged auth/JSON/Origin guards and the combined 60-call budget.").toEqual([]);
      expect.soft(formats.pageErrors).toEqual([]);
      await formats.assertPrivate(page);
      await testInfo.attach("report-formats-http-ledger", {
        body: JSON.stringify({
          requests: formats.requests.map((call) => ({
            ...call, body: call.path === "/api/v1/login" ? { credentials: "redacted synthetic sign-in" } :
              typeof call.body.report === "string" ? { ...call.body, report: {
                redacted: "Synthetic original report bytes are checked in memory; the ledger records only size and digest.",
                bytes: Buffer.byteLength(call.body.report), digest: digest(call.body.report),
              } } : call.body,
          })),
          violations: formats.violations, pageErrors: formats.pageErrors, externalAttempts: formats.externalAttempts,
          boundary: "HTTP response fixtures only; no backend/parser/worker/browser loader or React state is replaced.",
        }, null, 2), contentType: "application/json",
      });
    }
  },
});
export { expect } from "@playwright/test";
