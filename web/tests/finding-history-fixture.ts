import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import { bootstrapToken, password, sessionCookie, wrongPassword } from "./application-fixture";
import {
  apiVersion, backendID, betaHistoryFinding, findingPage, findingPath, historyAlpha, historyBeta,
  historyCookie, historyFinding, historyUser, newNoteID, notesPath, validNoteText, workItem,
} from "./finding-history-data";
import type { HistoryFinding, HistoryResponse, HistoryRole } from "./finding-history-data";

type Denial = 400 | 401 | 403 | 404 | 503;
export interface HistoryCall {
  method: string; path: string; workspace: string | undefined; query: Record<string, string>;
  body: Record<string, unknown>; status: number | null; response: Record<string, unknown> | null; failure: string | null;
}
export interface HistoryControl { call: HistoryCall | null; requested: Promise<void>; delivered: Promise<void>; release: () => void }
interface Gate extends HistoryControl { wait: Promise<void>; arrive: () => void; complete: () => void }
interface Reply { status: 200 | 201 | Denial; gate: Gate; query: Record<string, string>; workspace: string }
const secrets = [password, wrongPassword, bootstrapToken, sessionCookie, historyCookie];

export class FindingHistoryAPI {
  authenticated = true;
  readonly roles = new Map<string, HistoryRole>([[historyAlpha.id, "analyst"], [historyBeta.id, "viewer"]]);
  readonly serverRoles = new Map(this.roles);
  readonly findings = new Map<string, HistoryFinding>([
    [historyFinding.id, structuredClone(historyFinding)], [betaHistoryFinding.id, structuredClone(betaHistoryFinding)],
  ]);
  readonly requests: HistoryCall[] = [];
  readonly pages: Array<{ call: HistoryCall; response: HistoryResponse }> = [];
  readonly violations: string[] = [];
  readonly pageErrors: string[] = [];
  readonly consoleText: string[] = [];
  externalAttempts = 0;
  private replies = new Map<string, Reply[]>();
  private gates = new Set<Gate>();
  private byRequest = new Map<Request, HistoryCall>();
  private sessionCompleted = false;

  calls(method = "GET", path = findingPath) { return this.requests.filter((call) => call.method === method && call.path === path); }
  publishMetadata(fields: Pick<HistoryFinding, "workflowState" | "description" | "sourceFreshnessAt">) {
    Object.assign(this.findings.get(historyFinding.id)!, structuredClone(fields));
  }
  private queue(method: string, path: string, status: Reply["status"], held: boolean, query: Record<string, string>, workspace: string) {
    let release!: () => void, arrive!: () => void, complete!: () => void;
    const gate: Gate = {
      call: null, wait: new Promise<void>((resolve) => { release = resolve; }),
      requested: new Promise<void>((resolve) => { arrive = resolve; }),
      delivered: new Promise<void>((resolve) => { complete = resolve; }),
      release: () => release(), arrive: () => arrive(), complete: () => complete(),
    };
    const key = `${method} ${path}`;
    this.replies.set(key, [...(this.replies.get(key) ?? []), { status, gate, query, workspace }]);
    this.gates.add(gate); if (!held) release();
    return gate;
  }
  queueRead(status: 200 | Denial = 200, held = false, query: Record<string, string> = {}, workspace = historyAlpha.id) {
    return this.queue("GET", findingPath, status, held, query, workspace);
  }
  queueNote(held = false) { return this.queue("POST", notesPath, 201, held, {}, historyAlpha.id); }
  queueWorkflow(held = false) { return this.queue("PATCH", findingPath, 200, held, {}, historyAlpha.id); }
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
    return { apiVersion, error: { code, message, requestId: "synthetic-finding-history-request", retryable: false } };
  }
  private async respond(route: Route, call: HistoryCall, status: number, value: Record<string, unknown>, reply?: Reply) {
    call.status = status; call.response = structuredClone(value);
    try {
      if (reply) { reply.gate.call = call; reply.gate.arrive(); await reply.gate.wait; }
      if (status === 401) this.authenticated = false;
      await route.fulfill({ status, json: call.response });
    } finally {
      reply?.gate.complete(); if (reply) this.gates.delete(reply.gate);
    }
  }
  async assertPrivate(page: Page) {
    if (page.isClosed()) return;
    const state = await page.evaluate(() => ({
      text: document.body.textContent, cookie: document.cookie,
      storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
    }));
    for (const secret of secrets) {
      expect(JSON.stringify(state) + page.url() + this.consoleText.join("\n")).not.toContain(secret);
    }
    for (const [key, value] of state.storage) {
      expect(key, "No finding history, notes, cursor or credential state belongs in browser storage.").toBe("aspm.theme");
      expect(["light", "dark"]).toContain(value);
    }
  }
  async install(page: Page, origin: string) {
    const local = new URL(origin);
    if (local.protocol !== "http:" || local.hostname !== "127.0.0.1") throw new Error("Only the existing loopback browser fixture boundary is authorized.");
    await page.context().addCookies([{ name: "aspm_session", value: historyCookie, url: origin, httpOnly: true, sameSite: "Lax" }]);
    page.on("pageerror", (error) => this.pageErrors.push(error.message));
    page.on("console", (message) => this.consoleText.push(message.text()));
    page.on("requestfailed", (request) => {
      const call = this.byRequest.get(request); if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) {
        this.externalAttempts++; this.violations.push("No note/source URL, provider, proof, scanner or external HTTP request is authorized.");
        await route.abort(); return;
      }
      if (!url.pathname.startsWith("/api/")) { await route.continue(); return; }
      const headers = await request.allHeaders(), method = request.method(), path = url.pathname;
      const call: HistoryCall = {
        method, path, workspace: headers["x-aspm-workspace-id"], query: Object.fromEntries(url.searchParams),
        body: {}, status: null, response: null, failure: null,
      };
      this.requests.push(call); this.byRequest.set(request, call);
      if (this.requests.length > 60) {
        this.violations.push("The unchanged combined 60-API-call budget was exceeded.");
        await this.respond(route, call, 429, this.error(429)); return;
      }
      if (headers.authorization || headers["x-aspm-bootstrap-token"] ||
        secrets.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret)))) {
        this.violations.push("Finding history uses only the HttpOnly session, never bearer/provider/bootstrap/URL credentials.");
      }
      if (headers["if-match"] || headers["if-unmodified-since"] || headers["x-aspm-revision"]) {
        this.violations.push("The finding/history API has no server revision or conditional-read/write contract.");
      }
      const write = method === "PATCH" && path === findingPath ||
        method === "POST" && [notesPath, "/api/v1/login", "/api/v1/logout"].includes(path);
      if (method !== "GET" && !write || write && url.search !== "") {
        this.violations.push(`Undeclared history operation: ${method} ${path}.`);
        await route.abort(); return;
      }
      if (write) {
        if (headers.origin !== origin) this.violations.push("Writes require the matching Origin.");
        if (path !== "/api/v1/logout") {
          let parsed: unknown; try { parsed = request.postDataJSON(); } catch { parsed = null; }
          const media = headers["content-type"]?.split(";").map((part) => part.trim().toLowerCase());
          if (media?.[0] !== "application/json" ||
            media.some((part) => part.startsWith("charset=") && part !== "charset=utf-8") ||
            headers["content-encoding"] && headers["content-encoding"] !== "identity" ||
            parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
            this.violations.push("Finding mutations use the existing UTF-8 JSON object body.");
            await this.respond(route, call, 400, this.error(400)); return;
          }
          call.body = parsed as Record<string, unknown>;
          if ((request.postDataBuffer()?.length ?? 0) > (method === "PATCH" ? 16 << 10 : 32 << 10)) {
            this.violations.push("The original finding/note encoded-body bound was exceeded.");
            await this.respond(route, call, 413, this.error(413)); return;
          }
        }
      }
      const cookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${historyCookie}`);
      const session = () => ({
        apiVersion, user: historyUser, expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        workspaces: [historyAlpha, historyBeta].map((workspace) => ({ ...workspace, role: this.roles.get(workspace.id)! })),
      });
      if (method === "GET" && path === "/api/v1/session" && url.search === "") {
        if (this.authenticated && cookie) { await this.respond(route, call, 200, session()); this.sessionCompleted = true; }
        else await this.respond(route, call, 401, this.error(401));
        return;
      }
      if (method === "POST" && path === "/api/v1/login") {
        if (Object.keys(call.body).sort().join(",") !== "email,password" || call.body.email !== historyUser.email || call.body.password !== password) {
          await this.respond(route, call, 401, this.error(401));
        } else {
          this.authenticated = true; call.status = 200; call.response = session();
          await route.fulfill({ json: call.response, headers: { "set-cookie": `aspm_session=${historyCookie}; Path=/; HttpOnly; SameSite=Lax` } });
          this.sessionCompleted = true;
        }
        return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected history was requested before the authenticated session completed.");
      if (!this.authenticated || !cookie) { await this.respond(route, call, 401, this.error(401)); return; }
      if (method === "POST" && path === "/api/v1/logout") {
        this.authenticated = false; this.sessionCompleted = false; call.status = 204;
        await route.fulfill({ status: 204, headers: { "set-cookie": "aspm_session=; Max-Age=0; Path=/; HttpOnly; SameSite=Lax" } }); return;
      }
      const workspace = call.workspace;
      if (!workspace || !this.roles.has(workspace)) {
        this.violations.push("History requests require the selected session-owned workspace.");
        await this.respond(route, call, 403, this.error(403)); return;
      }
      const queue = this.replies.get(`${method} ${path}`) ?? [];
      const reply = queue[0];
      if (reply && (reply.workspace !== workspace || Object.entries(reply.query).some(([key, value]) => (call.query[key] ?? "") !== value))) {
        this.violations.push("The requested history cursor/workspace differs from the explicitly queued response.");
        await this.respond(route, call, 400, this.error(400)); return;
      }
      if (reply) queue.shift();
      if (!this.serverRoles.has(workspace)) { await this.respond(route, call, 403, this.error(403), reply); return; }
      const id = /^\/api\/v1\/findings\/([a-f0-9]{32})$/.exec(path)?.[1];
      const finding = id ? this.findings.get(id) : undefined;
      if (method === "GET" && id) {
        if (!finding || finding.workspaceId !== workspace) { await this.respond(route, call, 404, this.error(404), reply); return; }
        let response: HistoryResponse;
        try { response = findingPage(finding, url); } catch (error) {
          this.violations.push(String(error)); await this.respond(route, call, 400, this.error(400), reply); return;
        }
        const status = reply?.status ?? 200;
        if (status === 200) this.pages.push({ call, response: structuredClone(response) });
        await this.respond(route, call, status, status === 200 ? { ...response } : this.error(status), reply);
      } else if (method === "PATCH" && path === findingPath) {
        if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) { await this.respond(route, call, 403, this.error(403), reply); return; }
        const current = this.findings.get(historyFinding.id)!;
        if (current.workspaceId !== workspace) { await this.respond(route, call, 404, this.error(404), reply); return; }
        if (Object.keys(call.body).join(",") !== "workflowState" ||
          !["open", "in-progress", "resolved"].includes(String(call.body.workflowState))) {
          this.violations.push("This narrative permits only the intentional canonical workflow PATCH, not history or source fields.");
          await this.respond(route, call, 400, this.error(400), reply); return;
        }
        current.workflowState = call.body.workflowState as HistoryFinding["workflowState"];
        await this.respond(route, call, 200, { ...findingPage(current, url) }, reply);
      } else if (method === "POST" && path === notesPath) {
        if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) { await this.respond(route, call, 403, this.error(403), reply); return; }
        const current = this.findings.get(historyFinding.id)!;
        if (current.workspaceId !== workspace) { await this.respond(route, call, 404, this.error(404), reply); return; }
        if (Object.keys(call.body).join(",") !== "text" || !validNoteText(call.body.text)) {
          this.violations.push("Notes accept only literal nonblank/NUL-free text up to 8192 UTF-8 bytes.");
          await this.respond(route, call, 400, this.error(400), reply); return;
        }
        if (current.notes.some((note) => note.id === newNoteID)) throw new Error("Only one explicitly authored new-note intent belongs in this bounded scenario.");
        const note = { id: newNoteID, text: call.body.text }; current.notes.push(note);
        await this.respond(route, call, 201, { apiVersion, note }, reply);
      } else if (method === "GET" && path === "/api/v1/work") {
        const q = url.searchParams.get("q") ?? "";
        if ([...url.searchParams.keys()].some((key) => key !== "q") || Buffer.byteLength(q) > 512 || q.includes("\0")) {
          this.violations.push("No new Work history/search API is declared.");
          await this.respond(route, call, 400, this.error(400)); return;
        }
        const items = [...this.findings.values()].filter((item) => item.workspaceId === workspace &&
          `${item.title}\n${item.assetName}\n${item.ownerName ?? ""}`.toLowerCase().includes(q.toLowerCase())).map((item) => workItem({ ...item, observations: [] }));
        await this.respond(route, call, 200, { apiVersion, dataOrigin: "synthetic", items, total: items.length, nextCursor: null });
      } else if (method === "GET" && path === `${findingPath}/deliveries` && workspace === historyAlpha.id) {
        if ([...url.searchParams.keys()].some((key) => !["limit", "cursor"].includes(key)) ||
          url.searchParams.has("cursor") && url.searchParams.get("cursor") !== "" && !backendID(url.searchParams.get("cursor"))) {
          this.violations.push("Undeclared notification-history query."); await this.respond(route, call, 400, this.error(400)); return;
        }
        const limit = Number(url.searchParams.get("limit") || "100");
        if (!Number.isInteger(limit) || limit < 1 || limit > 500) {
          this.violations.push("Notification history limit must remain native."); await this.respond(route, call, 400, this.error(400)); return;
        }
        await this.respond(route, call, 200, { apiVersion, dataOrigin: "synthetic", items: [], total: 0, nextCursor: null });
      } else {
        this.violations.push(`Undeclared finding-history endpoint: ${method} ${path}.`);
        await this.respond(route, call, 404, this.error(404), reply);
      }
    });
  }
}

export const test = base.extend<{ historyAPI: FindingHistoryAPI }>({
  historyAPI: async ({ page, baseURL }, use, testInfo) => {
    if (!baseURL) throw new Error("Finding history requires the unchanged loopback runner.");
    const historyAPI = new FindingHistoryAPI(); await historyAPI.install(page, new URL(baseURL).origin);
    try { await use(historyAPI); } finally {
      await historyAPI.releaseResponses();
      expect.soft(historyAPI.violations, "Only declared same-origin history reads and bounded finding writes; existing auth/Origin/credential safeguards remain.").toEqual([]);
      expect.soft(historyAPI.pageErrors).toEqual([]);
      await historyAPI.assertPrivate(page);
      await testInfo.attach("finding-history-http-ledger", {
        body: JSON.stringify({
          requests: historyAPI.requests.map((call) => call.path === "/api/v1/login" ?
            { ...call, body: { credentials: "redacted synthetic sign-in" } } : call),
          violations: historyAPI.violations, pageErrors: historyAPI.pageErrors, externalAttempts: historyAPI.externalAttempts,
          boundary: "Only synthetic HTTP responses. No backend/React/client API state, DOM or native provider is replaced or invoked.",
        }, null, 2), contentType: "application/json",
      });
    }
  },
});
export { expect } from "@playwright/test";
