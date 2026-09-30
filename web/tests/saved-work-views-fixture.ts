import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request } from "@playwright/test";
import { WorkSearchAPI } from "./work-search-fixture";
import type { WorkCall, WorkControl, WorkEntry } from "./work-search-fixture";
import { alpha, cookie } from "./work-search-data";
import { backendID } from "./finding-actions-data";
import { apiVersion, viewPath, viewsPath } from "./saved-work-views-data";

export type { WorkCall, WorkControl, WorkEntry };
type Status = 200 | 201 | 204 | 400 | 401 | 403 | 404 | 409 | 503 | "network";
interface Options { status?: Status; held?: boolean; workspace?: string }
interface Gate extends WorkControl { wait: Promise<void>; complete: () => void }
interface Reply {
  query: Record<string, string>; body: Record<string, unknown>; payload: unknown;
  status: Status; gate: Gate; entry?: ViewEntry;
}
export interface ViewEntry extends WorkEntry { responses: WorkControl[] }
const keyFor = (method: string, path: string, workspace: string) => JSON.stringify([method, path, workspace]);
function errorBody(status: number) {
  return { apiVersion, error: {
    code: ({ 400: "invalid-input", 401: "unauthorized", 403: "forbidden", 404: "not-found", 409: "conflict", 503: "unavailable" } as Record<number, string>)[status],
    message: "Synthetic personal preference unavailable or denied.", requestId: "synthetic-personal-view", retryable: status === 503,
  } };
}

export class SavedViewsAPI extends WorkSearchAPI {
  readonly viewCalls: WorkCall[] = [];
  readonly viewEntries: ViewEntry[] = [];
  private readonly viewReplies = new Map<string, Reply[]>();
  private readonly viewRequests = new Map<Request, WorkCall>();
  private readonly viewGates = new Set<Gate>();
  private readonly openEntries = new Map<string, ViewEntry>();
  private readonly privateViewText = new Set<string>();

  remember(...values: string[]) { for (const value of values) if (value !== "") this.privateViewText.add(value); }
  private rememberPayload(payload: unknown) {
    if (payload === null || typeof payload !== "object") return;
    const body = payload as { view?: unknown; items?: unknown };
    for (const value of [body.view, ...Array.isArray(body.items) ? body.items : []]) {
      if (value === null || typeof value !== "object") continue;
      const view = value as Record<string, unknown>;
      for (const key of ["id", "name", "query"]) if (typeof view[key] === "string") this.remember(view[key]);
    }
  }
  private enqueueView(method: string, path: string, query: Record<string, string>, body: Record<string, unknown>,
    payload: unknown, options: Options, entry?: ViewEntry): WorkControl {
    const { status = 200, held = true, workspace = alpha.id } = options;
    if (!this.roles.has(workspace)) throw new Error("Declare a verified synthetic view workspace.");
    let release!: () => void, complete!: () => void;
    const gate: Gate = { call: null, wait: new Promise<void>((resolve) => { release = resolve; }),
      delivered: new Promise<void>((resolve) => { complete = resolve; }), release: () => release(), complete: () => complete() };
    if (!held) gate.release();
    this.viewGates.add(gate); this.rememberPayload(payload);
    const key = keyFor(method, path, workspace);
    const response = status === "network" ? null : status >= 400 ? errorBody(status) : payload;
    this.viewReplies.set(key, [...this.viewReplies.get(key) ?? [],
      { query, body: structuredClone(body), payload: structuredClone(response), status, gate, entry }]);
    return gate;
  }
  listViews(payload: unknown, options: Options & { cursor?: string } = {}) {
    const cursor = options.cursor ?? "";
    if (cursor !== "" && !backendID(cursor)) throw new Error("Only native view cursors are permitted.");
    return this.enqueueView("GET", viewsPath, cursor === "" ? {} : { limit: "100", cursor }, {}, payload, options);
  }
  viewsEntry(payload: unknown, workspace = alpha.id, held = true): ViewEntry {
    const key = keyFor("GET", viewsPath, workspace);
    if (this.openEntries.has(workspace) || this.viewReplies.get(key)?.length) throw new Error("Close the previous view entry first.");
    const entry: ViewEntry = { workspace, calls: [], closed: false, responses: [] };
    this.openEntries.set(workspace, entry); this.viewEntries.push(entry);
    entry.responses = [0, 1].map(() => this.enqueueView("GET", viewsPath, {}, {}, payload, { workspace, held }, entry));
    return entry;
  }
  finishViewsEntry(entry: ViewEntry) {
    expect(this.openEntries.get(entry.workspace)).toBe(entry); expect(entry.closed).toBe(false);
    expect(entry.calls.length).toBeGreaterThan(0); expect(entry.calls.length).toBeLessThanOrEqual(2);
    expect(entry.calls.some((call) => call.responseStatus === 200 && call.responseBeforeFailure &&
      call.finished && call.failure === null)).toBe(true);
    for (const call of entry.calls) {
      expect(call).toMatchObject({ method: "GET", path: viewsPath, workspace: entry.workspace, query: {}, body: {}, status: 200 });
      if (call.failure !== null) expect(call.failure).toMatch(/abort/i);
      else expect(call).toMatchObject({ responseStatus: 200, responseBeforeFailure: true, finished: true });
    }
    const key = keyFor("GET", viewsPath, entry.workspace);
    const unused = this.viewReplies.get(key) ?? [];
    for (const reply of unused) expect(reply.entry).toBe(entry);
    this.viewReplies.delete(key); this.openEntries.delete(entry.workspace); entry.closed = true;
  }
  detailView(id: string, payload: unknown, options: Options = {}) {
    if (!backendID(id)) throw new Error("Read only a native saved-view ID.");
    return this.enqueueView("GET", viewPath(id), {}, {}, payload, options);
  }
  createView(body: Record<string, unknown>, payload: unknown, options: Options = {}) {
    return this.enqueueView("POST", viewsPath, {}, body, payload, { status: 201, ...options });
  }
  patchView(id: string, body: Record<string, unknown>, payload: unknown, options: Options = {}) {
    if (!backendID(id)) throw new Error("Patch only a native saved-view ID.");
    return this.enqueueView("PATCH", viewPath(id), {}, body, payload, options);
  }
  deleteView(id: string, revision: string, options: Options = {}) {
    if (!backendID(id)) throw new Error("Delete only a native saved-view ID.");
    return this.enqueueView("DELETE", viewPath(id), {}, { revision }, null, { status: 204, ...options });
  }
  override async releaseResponses() {
    const arrived = [...this.viewGates].filter((gate) => gate.call !== null);
    for (const gate of this.viewGates) gate.release();
    await Promise.all([super.releaseResponses(), ...arrived.map((gate) => gate.delivered)]);
  }
  override async assertPrivate(page: Page) {
    await super.assertPrivate(page);
    if (page.isClosed() || !page.url().startsWith("http://127.0.0.1:")) return;
    const persisted = await page.evaluate(() => JSON.stringify({
      local: Object.entries(localStorage), session: Object.entries(sessionStorage), cookie: document.cookie,
    }));
    for (const value of this.privateViewText) {
      expect(persisted + this.consoleText.join("\n")).not.toContain(value);
      expect(decodeURIComponent(page.url())).not.toContain(value);
    }
  }
  override async install(page: Page, origin: string) {
    await super.install(page, origin);
    page.on("response", (response) => {
      const call = this.viewRequests.get(response.request());
      if (call) {
        call.responseStatus = response.status();
        call.responseBeforeFailure = call.failure === null && response.request().failure() === null;
      }
    });
    page.on("requestfinished", (request) => {
      const call = this.viewRequests.get(request); if (call) call.finished = true;
    });
    page.on("requestfailed", (request) => {
      const call = this.viewRequests.get(request); if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.context().route((url) => url.pathname === viewsPath || url.pathname.startsWith(`${viewsPath}/`), async (route) => {
      const request = route.request(), url = new URL(request.url());
      const call: WorkCall = { method: request.method(), path: url.pathname, workspace: undefined,
        query: Object.fromEntries(url.searchParams), body: {}, status: null, response: null,
        failure: request.failure()?.errorText ?? null, responseStatus: null, responseBeforeFailure: false, finished: false };
      this.requests.push(call); this.viewCalls.push(call); this.viewRequests.set(request, call);
      let reply: Reply | undefined;
      try {
        if (url.origin !== origin) { this.externalAttempts++; throw new Error("External view request blocked."); }
        const headers = await request.allHeaders();
        call.workspace = headers["x-aspm-workspace-id"];
        if (this.requests.length > 60) throw new Error("Combined 60 API request cap exceeded.");
        if (["authorization", "api-key", "x-api-key", "x-aspm-bootstrap-token", "if-match", "if-unmodified-since",
          "x-aspm-revision", "idempotency-key", "x-idempotency-key"].some((key) => headers[key])) {
          throw new Error("No extra credential, revision-header or create-idempotency authority is offered.");
        }
        if (url.searchParams.size !== Object.keys(call.query).length) throw new Error("Duplicate query selectors are forbidden.");
        const workspace = call.workspace;
        const hasCookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${cookie}`);
        if (!workspace || !this.roles.has(workspace) || !this.authenticated || !hasCookie ||
          !this.requests.some((read) => ["/api/v1/session", "/api/v1/login"].includes(read.path) &&
            read.responseStatus === 200 && read.responseBeforeFailure)) throw new Error("View traffic needs an authenticated scoped session.");
        if (call.method === "GET") {
          if (request.postData() !== null) throw new Error("View GET must have no body.");
        } else {
          if (headers.origin !== origin || !headers["content-type"]?.startsWith("application/json") ||
            (request.postDataBuffer()?.length ?? 0) > 16 << 10) throw new Error("View mutation needs bounded same-origin JSON.");
          const body: unknown = request.postDataJSON();
          if (body === null || typeof body !== "object" || Array.isArray(body)) throw new Error("View mutation requires an object.");
          call.body = body as Record<string, unknown>;
        }
        reply = this.viewReplies.get(keyFor(call.method, call.path, workspace))?.shift();
        if (!reply || !isDeepStrictEqual(call.query, reply.query) || !isDeepStrictEqual(call.body, reply.body)) {
          throw new Error("View HTTP lacked its exact declared method/path/scope/query/body intent.");
        }
        reply.entry?.calls.push(call);
        if (reply.entry && (reply.entry.closed || reply.entry.calls.length > 2)) throw new Error("View entry exceeded two declared calls.");
        call.status = reply.status === "network" ? null : reply.status;
        call.response = structuredClone(reply.payload); reply.gate.call = call;
        await reply.gate.wait;
        if (reply.status === 401) this.authenticated = false;
        if (reply.status === "network") await route.abort("failed");
        else if (reply.status === 204) await route.fulfill({ status: 204, body: "" });
        else await route.fulfill({ status: reply.status, json: call.response });
      } catch (cause) { this.violations.push(String(cause)); await route.abort(); }
      finally { reply?.gate.complete(); }
    });
  }
}

export const test = base.extend<{ viewsAPI: SavedViewsAPI }>({
  viewsAPI: async ({ page, baseURL }, use, info) => {
    if (!baseURL) throw new Error("Use the existing local browser server.");
    const api = new SavedViewsAPI();
    await api.install(page, new URL(baseURL).origin);
    try { await use(api); } finally {
      await api.releaseResponses(); api.assertData(); await api.assertPrivate(page);
      expect.soft(api.violations).toEqual([]); expect.soft(api.pageErrors).toEqual([]);
      expect.soft(api.externalAttempts).toBe(0); expect.soft(api.requests.length).toBeLessThanOrEqual(60);
      await info.attach("saved-work-views-http-ledger", { contentType: "application/json", body: JSON.stringify({
        authorLabel: "GPT-6 Astra; label only, not acceptance proof", reached: api.reached,
        requests: api.requests.map((call) => {
          const body = call.path === "/api/v1/login" ? { credentials: "redacted synthetic sign-in" } : call.body;
          const response = call.response as { items?: Array<{ id: string }>; total?: number; nextCursor?: string | null } | null;
          return { ...call, body, response: response?.items && !api.viewCalls.includes(call)
            ? { itemIDs: response.items.map((item) => item.id), total: response.total, nextCursor: response.nextCursor } : response };
        }),
        savedViewRequestIndexes: api.viewCalls.map((call) => api.requests.indexOf(call)),
        cohorts: [...api.entryCohorts, ...api.queryCohorts, ...api.denialCohorts, ...api.viewEntries].map((entry) => ({
          workspace: entry.workspace, q: "q" in entry ? entry.q : null, closed: entry.closed,
          kind: api.viewEntries.includes(entry as ViewEntry) ? "saved-view" : "inherited-Work",
          requestIndexes: entry.calls.map((call) => api.requests.indexOf(call)),
        })),
        violations: api.violations, pageErrors: api.pageErrors, externalAttempts: api.externalAttempts,
        combinedAPICalls: api.requests.length,
      }, null, 2) });
    }
  },
});
export { expect } from "@playwright/test";
