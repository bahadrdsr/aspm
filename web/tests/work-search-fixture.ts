import { expect, test as base } from "@playwright/test";
import type { Page, Request } from "@playwright/test";
import { WorkPaginationAPI } from "./work-pagination-fixture";
import type { WorkCall, WorkControl, WorkEntry } from "./work-pagination-fixture";
import { alpha, apiVersion, cookie, searchPage, searchParameters, validSearchQuery, workPath } from "./work-search-data";
import { backendID } from "./finding-actions-data";

export type { WorkCall, WorkControl, WorkEntry } from "./work-pagination-fixture";
type Status = Exclude<Parameters<WorkPaginationAPI["page"]>[1], undefined>;
interface Gate extends WorkControl { wait: Promise<void>; complete: () => void }
interface Reply { status: Status; gate: Gate; payload?: unknown }
export interface SearchEntry extends WorkEntry { q: string }
const keyFor = (q: string, cursor: string, workspace: string) => JSON.stringify([workspace, q, cursor]);

function errorBody(status: Exclude<Status, "network">) {
  const codes: Record<number, string> = {
    400: "invalid-input", 401: "unauthorized", 403: "forbidden", 404: "not-found", 503: "unavailable",
  };
  return { apiVersion, error: { code: codes[status], message: "Synthetic workspace search unavailable or denied.",
    requestId: "synthetic-work-search", retryable: status === 503 } };
}

export class WorkSearchAPI extends WorkPaginationAPI {
  readonly queryCohorts: SearchEntry[] = [];
  private readonly searchReplies = new Map<string, Reply[]>();
  private readonly searchGates = new Set<Gate>();
  private readonly searchRequests = new Map<Request, WorkCall>();
  private readonly searchEntries = new Map<string, SearchEntry>();
  private readonly privateQueries = new Set<string>();

  search(q: string, cursor = "", status: Status = 200, held = true, workspace = alpha.id, payload?: unknown): WorkControl {
    if (!validSearchQuery(q) || cursor !== "" && !backendID(cursor) || !this.roles.has(workspace)) {
      throw new Error("Declare only a valid native workspace query response.");
    }
    this.privateQueries.add(q);
    let release!: () => void, complete!: () => void;
    const gate: Gate = { call: null, wait: new Promise<void>((resolve) => { release = resolve; }),
      delivered: new Promise<void>((resolve) => { complete = resolve; }), release: () => release(), complete: () => complete() };
    this.searchGates.add(gate);
    if (!held) release();
    const key = keyFor(q, cursor, workspace);
    this.searchReplies.set(key, [...this.searchReplies.get(key) ?? [], { status, gate, payload: structuredClone(payload) }]);
    return gate;
  }

  queryEntry(q: string, workspace = alpha.id): SearchEntry {
    const key = keyFor(q, "", workspace);
    if (!validSearchQuery(q) || !this.roles.has(workspace) || this.searchEntries.has(key) || this.searchReplies.get(key)?.length) {
      throw new Error("Declare one bounded query entry without competing first-page replies.");
    }
    const entry = { q, workspace, calls: [], closed: false };
    this.privateQueries.add(q); this.queryCohorts.push(entry); this.searchEntries.set(key, entry);
    return entry;
  }

  finishQueryEntry(entry: SearchEntry) {
    const key = keyFor(entry.q, "", entry.workspace);
    expect(this.searchEntries.get(key)).toBe(entry); expect(entry.closed).toBe(false);
    expect(entry.calls.length).toBeGreaterThan(0); expect(entry.calls.length).toBeLessThanOrEqual(2);
    expect(entry.calls.some((call) => call.responseStatus === 200 && call.responseBeforeFailure &&
      call.failure === null && call.finished)).toBe(true);
    for (const call of entry.calls) {
      expect(call).toMatchObject({ method: "GET", path: workPath, workspace: entry.workspace, status: 200, body: {} });
      expect(call.query).toEqual({ q: entry.q });
      if (call.failure !== null) expect(call.failure).toMatch(/abort/i);
      else expect(call).toMatchObject({ responseStatus: 200, responseBeforeFailure: true, finished: true });
    }
    entry.closed = true; this.searchEntries.delete(key);
  }

  override async releaseResponses() {
    const arrived = [...this.searchGates].filter((gate) => gate.call !== null);
    for (const gate of this.searchGates) gate.release();
    await Promise.all([super.releaseResponses(), ...arrived.map((gate) => gate.delivered)]);
  }

  override async assertPrivate(page: Page) {
    await super.assertPrivate(page);
    if (page.isClosed() || !page.url().startsWith("http://127.0.0.1:")) return;
    const persisted = await page.evaluate(() => JSON.stringify({
      local: Object.entries(localStorage), session: Object.entries(sessionStorage), cookie: document.cookie,
    }));
    for (const q of this.privateQueries) {
      expect(persisted + this.consoleText.join("\n")).not.toContain(q);
      expect(decodeURIComponent(page.url())).not.toContain(q);
    }
  }

  override async install(page: Page, origin: string) {
    await super.install(page, origin);
    page.on("response", (response) => {
      const call = this.searchRequests.get(response.request());
      if (call) {
        call.responseStatus = response.status();
        call.responseBeforeFailure = call.failure === null && response.request().failure() === null;
      }
    });
    page.on("requestfinished", (request) => {
      const call = this.searchRequests.get(request);
      if (call) call.finished = true;
    });
    page.on("requestfailed", (request) => {
      const call = this.searchRequests.get(request);
      if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.context().route((url) => url.pathname === workPath && url.searchParams.has("q"), async (route) => {
      const request = route.request(), url = new URL(request.url());
      const call: WorkCall = { method: request.method(), path: url.pathname, workspace: undefined,
        query: Object.fromEntries(url.searchParams), body: {}, status: null, response: null,
        failure: request.failure()?.errorText ?? null, responseStatus: null, responseBeforeFailure: false, finished: false };
      this.requests.push(call); this.searchRequests.set(request, call);
      let reply: Reply | undefined;
      try {
        if (url.origin !== origin) { this.externalAttempts++; throw new Error("External Work search blocked."); }
        const headers = await request.allHeaders();
        call.workspace = headers["x-aspm-workspace-id"];
        if (this.requests.length > 60) throw new Error("Combined 60 API request cap exceeded.");
        if (call.method !== "GET" || request.postData() !== null ||
          ["authorization", "api-key", "x-api-key", "x-aspm-bootstrap-token", "if-match", "if-unmodified-since", "x-aspm-revision"]
            .some((name) => headers[name])) throw new Error("Work search must be read-only with existing cookie/workspace authority.");
        const { q, cursor } = searchParameters(url), workspace = call.workspace;
        const hasCookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${cookie}`);
        if (!workspace || !this.roles.has(workspace) || !this.authenticated || !hasCookie ||
          !this.requests.some((read) => ["/api/v1/session", "/api/v1/login"].includes(read.path) && read.responseStatus === 200)) {
          throw new Error("Search preceded an authenticated workspace session.");
        }
        const key = keyFor(q, cursor, workspace);
        reply = this.searchReplies.get(key)?.shift();
        if (!reply) {
          const entry = this.searchEntries.get(key);
          if (!entry || cursor !== "" || entry.calls.length >= 2) throw new Error("Search lacked an explicit response or bounded entry cohort.");
          entry.calls.push(call);
        }
        const status = this.serverRoles.has(workspace) ? reply?.status ?? 200 : 403;
        call.status = status === "network" ? null : status;
        call.response = structuredClone(status === 200 ? reply?.payload ?? searchPage(this.findings.values(), workspace, url) :
          errorBody(status === "network" ? 503 : status));
        if (reply) { reply.gate.call = call; await reply.gate.wait; }
        if (status === 401) this.authenticated = false;
        if (status === "network") await route.abort("failed");
        else await route.fulfill({ status, json: call.response });
      } catch (cause) { this.violations.push(String(cause)); await route.abort(); }
      finally { reply?.gate.complete(); }
    });
  }
}

export const test = base.extend<{ searchAPI: WorkSearchAPI }>({
  searchAPI: async ({ page, baseURL }, use, info) => {
    if (!baseURL) throw new Error("Use the existing local browser server.");
    const api = new WorkSearchAPI();
    await api.install(page, new URL(baseURL).origin);
    try { await use(api); } finally {
      await api.releaseResponses(); api.assertData(); await api.assertPrivate(page);
      expect.soft(api.violations).toEqual([]); expect.soft(api.pageErrors).toEqual([]);
      expect.soft(api.externalAttempts).toBe(0); expect.soft(api.requests.length).toBeLessThanOrEqual(60);
      await info.attach("work-search-http-ledger", { contentType: "application/json", body: JSON.stringify({
        reached: api.reached,
        requests: api.requests.map((call) => {
          const response = call.response as { items?: Array<{ id: string }>; total?: number; nextCursor?: string | null } | null;
          return { ...call, body: call.path === "/api/v1/login" ? { credentials: "redacted synthetic sign-in" } : call.body,
            response: response?.items ? { itemIDs: response.items.map((item) => item.id),
              total: response.total, nextCursor: response.nextCursor } : response };
        }),
        cohorts: [...api.entryCohorts, ...api.queryCohorts, ...api.denialCohorts].map((entry) => ({
          workspace: entry.workspace, q: "q" in entry ? entry.q : null, closed: entry.closed,
          requestIndexes: entry.calls.map((call) => api.requests.indexOf(call)),
        })),
        violations: api.violations, pageErrors: api.pageErrors, externalAttempts: api.externalAttempts,
        combinedAPICalls: api.requests.length,
      }, null, 2) });
    }
  },
});
export { expect } from "@playwright/test";
