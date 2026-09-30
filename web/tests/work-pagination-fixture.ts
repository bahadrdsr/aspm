import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import {
  alpha, apiVersion, backendID, beta, cookie, initialFindings, password, pathFor, user, workPage, workParameters, workPath,
} from "./work-pagination-data";
import type { ActionFinding, ActionRole } from "./work-pagination-data";

type Status = 200 | 400 | 401 | 403 | 404 | 503 | "network";
export interface WorkCall {
  method: string; path: string; workspace: string | undefined; query: Record<string, string>;
  body: Record<string, unknown>; status: number | null; response: unknown; failure: string | null;
  responseStatus: number | null; responseBeforeFailure: boolean; finished: boolean;
}
export interface WorkControl { call: WorkCall | null; delivered: Promise<void>; release: () => void }
export interface WorkEntry { calls: WorkCall[]; workspace: string; closed: boolean }
export interface WorkDetailDenial extends WorkEntry { path: string; responses: [WorkControl, WorkControl] }
interface Gate extends WorkControl { wait: Promise<void>; complete: () => void }
interface Reply { status: Status; gate: Gate; payload?: unknown; patch?: { body: Record<string, unknown>; canonical: ActionFinding } }

export class WorkPaginationAPI {
  authenticated = true;
  readonly roles = new Map<string, ActionRole>([[alpha.id, "analyst"], [beta.id, "viewer"]]);
  readonly serverRoles = new Map(this.roles);
  readonly findings = new Map(initialFindings().map((f) => [f.id, f]));
  readonly requests: WorkCall[] = [];
  readonly reached: string[] = [];
  readonly violations: string[] = [];
  readonly pageErrors: string[] = [];
  readonly consoleText: string[] = [];
  readonly entryCohorts: WorkEntry[] = [];
  readonly denialCohorts: WorkDetailDenial[] = [];
  externalAttempts = 0;
  private readonly original = structuredClone(this.findings);
  private readonly canonical = new Map<string, ActionFinding>();
  private readonly replies = new Map<string, Reply[]>();
  private readonly gates = new Set<Gate>();
  private readonly byRequest = new Map<Request, WorkCall>();
  private readonly entries = new Map<string, WorkEntry>();
  private readonly detailDenials = new Map<string, WorkDetailDenial>();
  private sessionCompleted = false;

  mark(value: string) { this.reached.push(value); }
  calls(method = "GET", path = workPath) { return this.requests.filter((call) => call.method === method && call.path === path); }
  entry(workspace = alpha.id): WorkEntry {
    if (this.entries.has(workspace)) throw new Error("Prior Work entry cohort was not confirmed.");
    const entry: WorkEntry = { calls: [], workspace, closed: false };
    this.entries.set(workspace, entry);
    this.entryCohorts.push(entry);
    return entry;
  }
  private finishCohort(entry: WorkEntry, path: string, status: 200 | 401) {
    expect(entry.closed).toBe(false);
    expect(entry.calls.length).toBeGreaterThan(0); expect(entry.calls.length).toBeLessThanOrEqual(2);
    const delivered = entry.calls.filter((call) => call.responseStatus === status && call.responseBeforeFailure &&
      (status === 401 || call.failure === null && call.finished));
    expect(delivered.length).toBeGreaterThan(0);
    for (const call of entry.calls) {
      expect(call).toMatchObject({ method: "GET", path, workspace: entry.workspace, status });
      expect(call.query).toEqual({}); expect(call.body).toEqual({});
      if (call.failure !== null) expect(call.failure).toMatch(/abort/i);
      else {
        expect(call).toMatchObject({ responseStatus: status, responseBeforeFailure: true });
        if (status === 200) expect(call.finished).toBe(true);
      }
    }
    entry.closed = true;
    return delivered.at(-1)!;
  }
  finishEntry(entry: WorkEntry, workspace = alpha.id) {
    expect(this.entries.get(workspace)).toBe(entry);
    const successful = this.finishCohort(entry, workPath, 200);
    this.entries.delete(workspace);
    return successful;
  }
  denyDetailEntry(id: string, workspace = alpha.id): WorkDetailDenial {
    const path = pathFor(id), key = `GET ${path}`;
    if (!backendID(id) || this.findings.get(id)?.workspaceId !== workspace ||
      this.detailDenials.has(path) || this.replies.get(key)?.length) {
      throw new Error("Declare one scoped detail-denial cohort without other pending detail replies.");
    }
    const entry: WorkDetailDenial = { calls: [], workspace, closed: false, path,
      responses: [this.detail(id, 401, true), this.detail(id, 401, true)] };
    this.detailDenials.set(path, entry);
    this.denialCohorts.push(entry);
    return entry;
  }
  finishDetailDenial(entry: WorkDetailDenial) {
    expect(this.detailDenials.get(entry.path)).toBe(entry);
    const successful = this.finishCohort(entry, entry.path, 401);
    const key = `GET ${entry.path}`;
    for (const reply of this.replies.get(key) ?? []) expect(entry.responses).toContain(reply.gate);
    this.replies.delete(key);
    this.detailDenials.delete(entry.path);
    return successful;
  }
  private schedule(key: string, status: Status, held: boolean, payload?: unknown, patch?: Reply["patch"]) {
    let release!: () => void, complete!: () => void;
    const gate: Gate = { call: null, wait: new Promise<void>((r) => { release = r; }),
      delivered: new Promise<void>((r) => { complete = r; }), release: () => release(), complete: () => complete() };
    this.gates.add(gate);
    if (!held) release();
    this.replies.set(key, [...this.replies.get(key) ?? [], { status, gate, payload: structuredClone(payload), patch }]);
    return gate;
  }
  page(cursor = "", status: Status = 200, held = true, workspace = alpha.id, payload?: unknown) {
    if (cursor !== "" && !backendID(cursor) || !this.roles.has(workspace)) throw new Error("Schedule only native scoped Work reads.");
    return this.schedule(`GET ${workPath} ${workspace} ${cursor}`, status, held, payload);
  }
  detail(id: string, status: Status = 200, held = false) { return this.schedule(`GET ${pathFor(id)}`, status, held); }
  patch(body: Record<string, unknown>, canonical: ActionFinding, held = false) {
    const before = this.findings.get(canonical.id);
    if (!before || Object.keys(body).length !== 1 || !["ownerId", "workflowState"].includes(Object.keys(body)[0])) {
      throw new Error("Only one declared existing owner/workflow intent may be acknowledged.");
    }
    const unchanged = (value: ActionFinding) => {
      const { ownerId: _owner, ownerName: _name, workflowState: _workflow, ...source } = value;
      return source;
    };
    if (!isDeepStrictEqual(unchanged(before), unchanged(canonical)) ||
      "ownerId" in body && (canonical.ownerId !== body.ownerId || canonical.workflowState !== before.workflowState) ||
      "workflowState" in body && (canonical.workflowState !== body.workflowState ||
        canonical.ownerId !== before.ownerId || canonical.ownerName !== before.ownerName)) {
      throw new Error("ACK metadata may not alter unrequested decisions or source facts.");
    }
    return this.schedule(`PATCH ${pathFor(canonical.id)}`, 200, held, undefined,
      { body: structuredClone(body), canonical: structuredClone(canonical) });
  }
  async releaseResponses() {
    const arrived = [...this.gates].filter((g) => g.call !== null);
    for (const g of this.gates) g.release();
    await Promise.all(arrived.map((g) => g.delivered));
  }
  private error(status: number) {
    const code = ({ 400: "invalid-input", 401: "unauthorized", 403: "forbidden", 404: "not-found", 503: "unavailable" } as Record<number, string>)[status];
    return { apiVersion, error: { code, message: "Synthetic Work read unavailable or access denied.",
      requestId: "synthetic-work-pagination", retryable: false } };
  }
  private async respond(route: Route, call: WorkCall, status: Status, payload: unknown, reply?: Reply) {
    call.status = status === "network" ? null : status;
    call.response = structuredClone(payload);
    try {
      if (reply) { reply.gate.call = call; await reply.gate.wait; }
      if (status === 401) this.authenticated = false;
      if (status === "network") await route.abort("failed");
      else await route.fulfill({ status, json: call.response });
    } finally { reply?.gate.complete(); }
  }
  async assertPrivate(page: Page) {
    if (page.isClosed() || !page.url().startsWith("http://127.0.0.1:")) return;
    const state = await page.evaluate(() => ({
      text: document.body.textContent, cookie: document.cookie, storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
    }));
    for (const [key, value] of state.storage) { expect(key).toBe("aspm.theme"); expect(["light", "dark"]).toContain(value); }
    for (const value of [cookie, password]) {
      expect(JSON.stringify(state) + page.url() + this.consoleText.join("\n")).not.toContain(value);
    }
    const persisted = JSON.stringify(state.storage) + state.cookie + this.consoleText.join("\n");
    for (const id of this.findings.keys()) expect(persisted).not.toContain(id);
  }
  async install(page: Page, origin: string) {
    if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error("Only local Vite assets are authorized.");
    await page.context().addCookies([{ name: "aspm_session", value: cookie, url: origin, httpOnly: true, sameSite: "Lax" }]);
    page.on("pageerror", (e) => this.pageErrors.push(e.message));
    page.on("console", (e) => this.consoleText.push(e.text()));
    page.on("response", (response) => {
      const call = this.byRequest.get(response.request());
      if (call) {
        call.responseStatus = response.status();
        call.responseBeforeFailure = call.failure === null && response.request().failure() === null;
      }
    });
    page.on("requestfinished", (request) => {
      const call = this.byRequest.get(request);
      if (call) call.finished = true;
    });
    page.on("requestfailed", (request) => {
      const call = this.byRequest.get(request);
      if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.context().routeWebSocket("**/*", (socket) => {
      const url = new URL(socket.url());
      if (url.protocol !== "ws:" || `http://${url.host}` !== origin || url.pathname !== "/") {
        this.externalAttempts++; this.violations.push("Undeclared WebSocket blocked."); socket.close(); return;
      }
      socket.connectToServer();
    });
    await page.context().route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url()), method = request.method(), path = url.pathname;
      if (url.origin !== origin) { this.externalAttempts++; this.violations.push("External HTTP blocked."); await route.abort(); return; }
      if (!path.startsWith("/api/")) {
        if (method !== "GET" || !(path === "/" || path === "/index.html" ||
          /^\/(?:src\/|node_modules\/|\.cache\/vite\/deps\/|@vite\/|@react-refresh$|@fs\/|@id\/|tests\/harness\/)/.test(path))) {
          this.violations.push("Undeclared non-API asset blocked."); await route.abort(); return;
        }
        await route.continue(); return;
      }
      const call: WorkCall = { method, path, workspace: undefined, query: Object.fromEntries(url.searchParams),
        body: {}, status: null, response: null, failure: request.failure()?.errorText ?? null,
        responseStatus: null, responseBeforeFailure: false, finished: false };
      this.requests.push(call); this.byRequest.set(request, call);
      const headers = await request.allHeaders();
      call.workspace = headers["x-aspm-workspace-id"];
      if (this.requests.length > 60) { this.violations.push("Combined 60 API request cap exceeded."); await route.abort(); return; }
      if (["authorization", "api-key", "x-api-key", "x-aspm-bootstrap-token", "if-match", "if-unmodified-since", "x-aspm-revision"]
        .some((key) => headers[key])) this.violations.push("Undeclared credentials or revision authority.");
      const id = /^\/api\/v1\/findings\/([a-f0-9]{32})$/.exec(path)?.[1];
      const write = method === "PATCH" && id !== undefined || method === "POST" && ["/api/v1/login", "/api/v1/logout"].includes(path);
      if (method !== "GET" && !write || path !== workPath && url.search !== "") {
        this.violations.push("Undeclared route/body/query operation."); await route.abort(); return;
      }
      if (write) {
        if (headers.origin !== origin) this.violations.push("Write Origin does not match the application.");
        if (path !== "/api/v1/logout") {
          try {
            const body: unknown = request.postDataJSON();
            if (body === null || typeof body !== "object" || Array.isArray(body) ||
              !headers["content-type"]?.startsWith("application/json") || (request.postDataBuffer()?.length ?? 0) > 16 << 10) throw new Error();
            call.body = body as Record<string, unknown>;
          } catch { this.violations.push("Declared writes require bounded JSON objects."); await route.abort(); return; }
        }
      } else if (request.postData() !== null) this.violations.push("Work/detail GET must not carry a body.");
      const hasCookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${cookie}`);
      const session = () => ({ apiVersion, user, expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        workspaces: [alpha, beta].map((w) => ({ ...w, role: this.roles.get(w.id)! })) });
      if (method === "GET" && path === "/api/v1/session") {
        if (this.authenticated && hasCookie) { await this.respond(route, call, 200, session()); this.sessionCompleted = true; }
        else await this.respond(route, call, 401, this.error(401));
        return;
      }
      if (method === "POST" && path === "/api/v1/login") {
        if (!isDeepStrictEqual(call.body, { email: user.email, password })) { await this.respond(route, call, 401, this.error(401)); return; }
        this.authenticated = true; call.status = 200; call.response = session();
        await route.fulfill({ status: 200, json: call.response, headers: { "set-cookie": `aspm_session=${cookie}; Path=/; HttpOnly; SameSite=Lax` } });
        this.sessionCompleted = true; return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected traffic preceded verified session completion.");
      const denial = method === "GET" ? this.detailDenials.get(path) : undefined;
      if (denial) {
        const reply = this.replies.get(`GET ${path}`)?.shift();
        if (!hasCookie || call.workspace !== denial.workspace || denial.calls.length >= 2 ||
          reply?.status !== 401 || !denial.responses.includes(reply.gate)) {
          this.violations.push("Detail denial exceeded its two declared scoped responses."); await route.abort(); return;
        }
        denial.calls.push(call);
        await this.respond(route, call, 401, this.error(401), reply); return;
      }
      if (!this.authenticated || !hasCookie) { await this.respond(route, call, 401, this.error(401)); return; }
      if (method === "POST" && path === "/api/v1/logout") {
        this.authenticated = false; this.sessionCompleted = false; call.status = 204;
        await route.fulfill({ status: 204, headers: { "set-cookie": "aspm_session=; Max-Age=0; Path=/; HttpOnly; SameSite=Lax" } }); return;
      }
      const workspace = call.workspace;
      if (!workspace || !this.roles.has(workspace)) {
        this.violations.push("Missing/unverified workspace authority."); await this.respond(route, call, 403, this.error(403)); return;
      }
      try {
        if (method === "GET" && path === workPath) {
          const { cursor } = workParameters(url);
          const reply = this.replies.get(`GET ${workPath} ${workspace} ${cursor}`)?.shift();
          if (!reply) {
            const entry = this.entries.get(workspace);
            if (cursor !== "" || !entry || entry.calls.length >= 2) {
              throw new Error("Work read lacked an explicitly scheduled initial/refresh/continuation action.");
            }
            entry.calls.push(call);
            const status = this.serverRoles.has(workspace) ? 200 : 403;
            await this.respond(route, call, status, status === 200 ? workPage(this.findings.values(), workspace, url) : this.error(status));
            return;
          }
          const status = this.serverRoles.has(workspace) ? reply.status : 403;
          const payload = status === 200 ? reply.payload ?? workPage(this.findings.values(), workspace, url) : this.error(status === "network" ? 503 : status);
          await this.respond(route, call, status, payload, reply); return;
        }
        if (id) {
          const finding = this.findings.get(id), reply = this.replies.get(`${method} ${path}`)?.shift();
          if (!finding || finding.workspaceId !== workspace) { await this.respond(route, call, 404, this.error(404), reply); return; }
          if (method === "GET") {
            const status = reply?.status ?? 200;
            await this.respond(route, call, status, status === 200 ? { apiVersion, dataOrigin: "synthetic", finding } : this.error(status === "network" ? 503 : status), reply); return;
          }
          if (!reply?.patch || !isDeepStrictEqual(call.body, reply.patch.body)) throw new Error("Finding PATCH lacked its exact explicit declared intent.");
          if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) {
            await this.respond(route, call, 403, this.error(403), reply); return;
          }
          this.findings.set(id, structuredClone(reply.patch.canonical));
          this.canonical.set(id, structuredClone(reply.patch.canonical));
          await this.respond(route, call, 200, { apiVersion, dataOrigin: "synthetic", finding: reply.patch.canonical }, reply); return;
        }
        throw new Error("Undeclared HTTP: no AI, Slack, source-history, provider or other API is authorized.");
      } catch (cause) { this.violations.push(String(cause)); await route.abort(); }
    });
  }
  assertData() {
    for (const [id, value] of this.original) {
      expect(this.findings.get(id), "Only an explicitly declared canonical PATCH may change fixture records.").toEqual(this.canonical.get(id) ?? value);
    }
  }
}

export const test = base.extend<{ workAPI: WorkPaginationAPI }>({
  workAPI: async ({ page, baseURL }, use, info) => {
    if (!baseURL) throw new Error("Use the original local browser server.");
    const api = new WorkPaginationAPI();
    await api.install(page, new URL(baseURL).origin);
    try { await use(api); } finally {
      await api.releaseResponses();
      api.assertData(); await api.assertPrivate(page);
      expect.soft(api.violations).toEqual([]); expect.soft(api.pageErrors).toEqual([]);
      expect.soft(api.externalAttempts).toBe(0); expect.soft(api.requests.length).toBeLessThanOrEqual(60);
      await info.attach("work-pagination-http-ledger", { contentType: "application/json", body: JSON.stringify({
        reached: api.reached, requests: api.requests.map((call) => call.path === "/api/v1/login"
          ? { ...call, body: { credentials: "redacted synthetic sign-in" } } : call),
        cohorts: {
          work: api.entryCohorts.map((entry) => ({ workspace: entry.workspace, closed: entry.closed,
            requestIndexes: entry.calls.map((call) => api.requests.indexOf(call)) })),
          detailDenial: api.denialCohorts.map((entry) => ({ path: entry.path, workspace: entry.workspace, closed: entry.closed,
            requestIndexes: entry.calls.map((call) => api.requests.indexOf(call)) })),
        },
        violations: api.violations, pageErrors: api.pageErrors, externalAttempts: api.externalAttempts, combinedAPICalls: api.requests.length,
      }, null, 2) });
    }
  },
});
export { expect } from "@playwright/test";
