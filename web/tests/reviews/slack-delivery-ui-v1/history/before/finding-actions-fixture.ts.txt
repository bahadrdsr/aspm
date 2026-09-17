import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import { bootstrapToken, password, sessionCookie, wrongPassword } from "./application-fixture";
import {
  actionAlpha, actionBeta, actionCookie, actionUser, apiVersion, backendID, betaFinding, companionFinding,
  currentOwner, findingPath, notesPath, primaryFinding, serverNow, validExpiry, validNoteText, workItem, workPath,
} from "./finding-actions-data";
import type { ActionFinding, ActionFindingResponse, ActionNote, ActionRole, ActionWorkResponse } from "./finding-actions-data";

type Denial = 400 | 401 | 403 | 503;
type Payload = Record<string, unknown> | ActionFindingResponse | ActionWorkResponse;
export interface FindingActionCall {
  method: string;
  path: string;
  workspace: string | undefined;
  query: Record<string, string>;
  body: Record<string, unknown>;
  status: number | null;
  response: Payload | null;
  failure: string | null;
}
export interface FindingActionControl {
  requested: Promise<void>;
  delivered: Promise<void>;
  release: () => void;
  call: FindingActionCall | null;
}
interface Gate extends FindingActionControl {
  wait: Promise<void>;
  arrive: () => void;
  complete: () => void;
}
interface Scheduled { status: number; gate: Gate }
const secretCanaries = [password, wrongPassword, bootstrapToken, sessionCookie, actionCookie];
const knownWorkspaces = [actionAlpha, actionBeta];

export class FindingActionsAPI {
  authenticated = true;
  readonly roles = new Map<string, ActionRole>([[actionAlpha.id, "analyst"], [actionBeta.id, "analyst"]]);
  readonly serverRoles = new Map(this.roles);
  readonly requests: FindingActionCall[] = [];
  readonly violations: string[] = [];
  readonly findings = new Map<string, ActionFinding>();
  readonly members = new Map([
    [actionAlpha.id, new Map([[actionUser.id, actionUser.name], [currentOwner.id, currentOwner.name]])],
    [actionBeta.id, new Map([[actionUser.id, actionUser.name]])],
  ]);
  private sessionCompleted = false;
  private noteSequence = 0;
  private gates = new Set<Gate>();
  private scheduled = new Map<string, Scheduled[]>();
  private byRequest = new Map<Request, FindingActionCall>();

  constructor() {
    for (const finding of [primaryFinding, companionFinding, betaFinding]) this.seedFinding(finding);
  }

  calls(method: string, path: string) {
    return this.requests.filter((call) => call.method === method && call.path === path);
  }

  seedFinding(finding: ActionFinding) {
    if (!backendID(finding.id) || !this.roles.has(finding.workspaceId) ||
      finding.ownerId !== null && !this.members.get(finding.workspaceId)?.has(finding.ownerId) ||
      finding.notes.some((note) => !backendID(note.id) || !validNoteText(note.text)) ||
      new Set(finding.notes.map((note) => note.id)).size !== finding.notes.length ||
      finding.notesNextCursor !== null || finding.observationsNextCursor !== null) {
      throw new Error("Seed complete synthetic server records with valid membership, note IDs and text; cursors are derived, not invented.");
    }
    this.findings.set(finding.id, structuredClone(finding));
  }

  seedQueueRows(count: number) {
    if (!Number.isInteger(count) || count < 0 || count > 40) throw new Error("This slice does not add Work pagination.");
    for (let index = 0; index < count; index++) this.seedFinding({
      ...primaryFinding,
      id: `4000000000000000${(index + 1).toString(16).padStart(16, "0")}`,
      title: `Triage alpha queue entry ${String(index + 1).padStart(2, "0")}`,
    });
  }

  finding(id = primaryFinding.id) {
    const finding = this.findings.get(id);
    if (!finding) throw new Error("Unknown synthetic finding.");
    return structuredClone(finding);
  }

  private control(held: boolean): Gate {
    let release!: () => void, arrive!: () => void, complete!: () => void;
    const wait = new Promise<void>((resolve) => { release = resolve; });
    const requested = new Promise<void>((resolve) => { arrive = resolve; });
    const delivered = new Promise<void>((resolve) => { complete = resolve; });
    const gate: Gate = { wait, requested, delivered, release, arrive, complete, call: null };
    this.gates.add(gate);
    if (!held) release();
    return gate;
  }

  private queue(key: string, status: number, held: boolean): FindingActionControl {
    const gate = this.control(held);
    this.scheduled.set(key, [...(this.scheduled.get(key) ?? []), { status, gate }]);
    return gate;
  }

  queuePatch(status: 200 | Denial = 200, held = false) {
    return this.queue(`PATCH ${findingPath}`, status, held);
  }

  queueNote(status: 201 | Denial = 201, held = false) {
    return this.queue(`POST ${notesPath}`, status, held);
  }

  holdWork(workspace: string) {
    if (!this.roles.has(workspace)) throw new Error("Only a known workspace can hold its Work response.");
    return this.queue(`GET ${workPath} ${workspace}`, 200, true);
  }

  async releaseResponses() {
    const requested = [...this.gates].filter((gate) => gate.call !== null);
    for (const gate of this.gates) gate.release();
    await Promise.all(requested.map((gate) => gate.delivered));
  }

  private failure(status: number) {
    const code = status === 401 ? "unauthorized" : status === 403 ? "forbidden" :
      status === 404 ? "not-found" : status === 400 ? "invalid-input" :
      status === 413 ? "too-large" : status === 429 ? "rate-limited" : "unavailable";
    return { apiVersion, error: {
      code,
      message: status === 401 ? "Synthetic triage session ended. Sign in again." :
        status === 403 ? "Synthetic triage access denied." :
          status === 400 ? "Synthetic finding input is invalid." : "Synthetic finding action could not be confirmed.",
      requestId: "synthetic-finding-action-request", retryable: false,
    } };
  }

  private async deliver(route: Route, call: FindingActionCall, status: number, response: Payload, gate?: Gate) {
    const snapshot = structuredClone(response);
    call.status = status;
    call.response = snapshot;
    try {
      if (gate) { gate.call = call; gate.arrive(); await gate.wait; }
      if (status === 401) this.authenticated = false;
      await route.fulfill({ status, json: snapshot });
    } finally {
      gate?.complete();
      if (gate) this.gates.delete(gate);
    }
  }

  private detail(finding: ActionFinding, url: URL): ActionFindingResponse {
    const notesCursor = url.searchParams.get("notesCursor") ?? "";
    const observationsCursor = url.searchParams.get("observationsCursor") ?? "";
    if ([...url.searchParams.keys()].some((key) => !["notesCursor", "observationsCursor"].includes(key)) ||
      notesCursor !== "" && !backendID(notesCursor) || observationsCursor !== "" && !backendID(observationsCursor)) {
      throw new Error("Finding reads support only the existing valid notesCursor and observationsCursor.");
    }
    const notes = finding.notes.filter((note) => note.id > notesCursor).sort((a, b) => a.id.localeCompare(b.id));
    const observations = finding.observations.filter((item) => item.id > observationsCursor).sort((a, b) => a.id.localeCompare(b.id));
    return { apiVersion, dataOrigin: "synthetic", finding: {
      ...structuredClone(finding),
      notes: notes.slice(0, 500),
      observations: observations.slice(0, 500),
      notesNextCursor: notes.length > 500 ? notes[499].id : null,
      observationsNextCursor: observations.length > 500 ? observations[499].id : null,
    } };
  }

  private work(workspace: string, url: URL): ActionWorkResponse {
    const q = url.searchParams.get("q") ?? "", cursor = url.searchParams.get("cursor") ?? "";
    const rawLimit = url.searchParams.get("limit");
    const limit = rawLimit === null ? 100 : Number(rawLimit);
    if ([...url.searchParams.keys()].some((key) => !["q", "limit", "cursor"].includes(key)) ||
      Buffer.byteLength(q, "utf8") > 512 || q.includes("\0") ||
      rawLimit !== null && !/^[+-]?\d+$/.test(rawLimit) ||
      !Number.isInteger(limit) || limit < 1 || limit > 500 || cursor !== "" && !backendID(cursor)) {
      throw new Error("Work reads use q and the existing bounded limit/cursor, not an invented filtering API.");
    }
    const all = [...this.findings.values()].filter((finding) => finding.workspaceId === workspace &&
      `${finding.title}\n${finding.assetName}\n${finding.ownerName ?? ""}`.toLowerCase().includes(q.toLowerCase()))
      .sort((a, b) => a.id.localeCompare(b.id));
    const remaining = all.filter((finding) => finding.id > cursor);
    const items = remaining.slice(0, limit).map(workItem);
    return { apiVersion, dataOrigin: "synthetic", items, total: all.length, nextCursor: remaining.length > limit ? items.at(-1)!.id : null };
  }

  private patch(finding: ActionFinding, body: Record<string, unknown>): ActionFinding | null {
    if (Object.keys(body).some((key) => !["ownerId", "workflowState", "disposition", "acceptedRiskExpiresAt"].includes(key))) {
      this.violations.push("PATCH accepts selected owner/workflow/disposition/expiry fields only, not UI authority or revision fields.");
      return null;
    }
    const next = structuredClone(finding);
    if ("ownerId" in body) {
      if (body.ownerId !== null && !backendID(body.ownerId)) return null;
      next.ownerId = body.ownerId as string | null;
    }
    if (next.ownerId !== null && !this.members.get(next.workspaceId)?.has(next.ownerId)) return null;
    next.ownerName = next.ownerId === null ? null : this.members.get(next.workspaceId)!.get(next.ownerId)!;
    if ("workflowState" in body) {
      if (typeof body.workflowState !== "string" || !["open", "in-progress", "resolved"].includes(body.workflowState)) return null;
      next.workflowState = body.workflowState as ActionFinding["workflowState"];
    }
    if ("disposition" in body) {
      if (typeof body.disposition !== "string" || !["none", "accepted-risk"].includes(body.disposition)) return null;
      next.disposition = body.disposition as ActionFinding["disposition"];
      if (next.disposition === "none" && !("acceptedRiskExpiresAt" in body)) next.acceptedRiskExpiresAt = null;
    }
    if ("acceptedRiskExpiresAt" in body) {
      const value = body.acceptedRiskExpiresAt;
      if (value !== null && !validExpiry(value)) return null;
      next.acceptedRiskExpiresAt = value as string | null;
    }
    if (next.acceptedRiskExpiresAt !== null && next.disposition !== "accepted-risk") return null;
    next.riskAcceptanceExpired = next.disposition === "accepted-risk" && next.acceptedRiskExpiresAt !== null &&
      Date.parse(next.acceptedRiskExpiresAt) <= Date.parse(serverNow);
    return next;
  }

  async install(page: Page, origin: string) {
    if (new URL(origin).hostname !== "127.0.0.1" || new URL(origin).protocol !== "http:") {
      throw new Error("Finding actions authorize only the existing loopback HTTP browser-test boundary.");
    }
    await page.context().addCookies([{ name: "aspm_session", value: actionCookie, url: origin, httpOnly: true, sameSite: "Lax" }]);
    page.on("requestfailed", (request) => {
      const call = this.byRequest.get(request);
      if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) {
        this.violations.push("Undeclared external traffic: this finding slice authorizes no native, cloud, provider or remote API.");
        await route.abort();
        return;
      }
      if (secretCanaries.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret)))) {
        this.violations.push("A synthetic credential appeared in a request URL.");
      }
      if (!url.pathname.startsWith("/api/")) { await route.continue(); return; }
      const headers = await request.allHeaders(), method = request.method(), path = url.pathname;
      const call: FindingActionCall = {
        method, path, workspace: headers["x-aspm-workspace-id"], query: Object.fromEntries(url.searchParams),
        body: {}, status: null, response: null, failure: null,
      };
      this.requests.push(call);
      this.byRequest.set(request, call);
      if (this.requests.length > 60) {
        this.violations.push("The unchanged combined 60-API-request budget was exceeded.");
        await this.deliver(route, call, 429, this.failure(429));
        return;
      }
      if (headers.authorization || headers["x-aspm-bootstrap-token"]) {
        this.violations.push("Finding actions require the HttpOnly session, never bearer, provider or bootstrap credentials.");
      }
      if (headers["if-match"] || headers["if-unmodified-since"] || headers["x-aspm-revision"]) {
        this.violations.push("The finding API has no conditional-write/revision contract.");
      }
      const declaredWrite = method === "PATCH" && path === findingPath ||
        method === "POST" && [notesPath, "/api/v1/login", "/api/v1/logout"].includes(path);
      if (method !== "GET" && !declaredWrite) {
        this.violations.push(`Undeclared finding write: ${method} ${path}.`);
        await route.abort();
        return;
      }
      if (declaredWrite) {
        if (headers.origin !== origin || url.search !== "") {
          this.violations.push("Writes require the matching Origin and fields in JSON, not in a URL.");
        }
        if (path !== "/api/v1/logout") {
          let parsed: unknown;
          try { parsed = request.postDataJSON(); } catch { parsed = null; }
          if (!headers["content-type"]?.startsWith("application/json") ||
            parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
            this.violations.push("Finding writes require a JSON object.");
            await this.deliver(route, call, 400, this.failure(400));
            return;
          }
          call.body = parsed as Record<string, unknown>;
          const limit = method === "PATCH" ? 16 << 10 : 32 << 10;
          if (Buffer.byteLength(request.postData() ?? "", "utf8") > limit) {
            await this.deliver(route, call, 413, this.failure(413));
            return;
          }
        }
      }
      const cookie = headers.cookie?.split(";").some((value) => value.trim() === `aspm_session=${actionCookie}`);
      const session = () => ({
        apiVersion, user: actionUser, expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        workspaces: knownWorkspaces.map((workspace) => ({ ...workspace, role: this.roles.get(workspace.id)! })),
      });
      if (path === "/api/v1/session" && method === "GET") {
        if (this.authenticated && cookie) {
          await this.deliver(route, call, 200, session());
          this.sessionCompleted = true;
        } else await this.deliver(route, call, 401, this.failure(401));
        return;
      }
      if (path === "/api/v1/login" && method === "POST") {
        if (Object.keys(call.body).sort().join(",") !== "email,password" ||
          call.body.email !== actionUser.email || call.body.password !== password) {
          await this.deliver(route, call, 401, this.failure(401));
        } else {
          this.authenticated = true;
          const response = session();
          call.status = 200; call.response = response;
          await route.fulfill({ json: response, headers: { "set-cookie": `aspm_session=${actionCookie}; Path=/; HttpOnly; SameSite=Lax` } });
          this.sessionCompleted = true;
        }
        return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected finding traffic began before the authenticated session completed.");
      if (!this.authenticated || !cookie) {
        await this.deliver(route, call, 401, this.failure(401));
        return;
      }
      if (path === "/api/v1/logout" && method === "POST") {
        this.authenticated = false; this.sessionCompleted = false;
        call.status = 204;
        await route.fulfill({ status: 204, headers: { "set-cookie": "aspm_session=; Max-Age=0; Path=/; HttpOnly; SameSite=Lax" } });
        return;
      }
      const workspace = call.workspace;
      if (!workspace || !this.roles.has(workspace)) {
        this.violations.push("Protected finding traffic omitted or invented the session-owned workspace header.");
        await this.deliver(route, call, 403, this.failure(403));
        return;
      }
      if (method === "GET" && path === workPath) {
        const scheduled = this.scheduled.get(`GET ${workPath} ${workspace}`)?.shift();
        try { await this.deliver(route, call, 200, this.work(workspace, url), scheduled?.gate); } catch (error) {
          this.violations.push(String(error));
          throw error;
        }
        return;
      }
      const id = path.split("/")[4];
      const finding = this.findings.get(id);
      if (method === "GET" && path === `/api/v1/findings/${id}` && backendID(id)) {
        if (!finding || finding.workspaceId !== workspace) await this.deliver(route, call, 404, this.failure(404));
        else {
          try { await this.deliver(route, call, 200, this.detail(finding, url)); } catch (error) {
            this.violations.push(String(error));
            throw error;
          }
        }
        return;
      }
      if (declaredWrite && [findingPath, notesPath].includes(path)) {
        const scheduled = this.scheduled.get(`${method} ${path}`)?.shift();
        let status = scheduled?.status ?? (method === "PATCH" ? 200 : 201);
        let response: Payload = this.failure(status);
        if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) status = 403;
        else if (!finding || finding.workspaceId !== workspace) status = 404;
        if (status === 200 && finding) {
          const updated = this.patch(finding, call.body);
          if (updated) {
            this.findings.set(id, updated);
            response = this.detail(updated, url);
          } else status = 400;
        } else if (status === 201 && finding) {
          if (Object.keys(call.body).join(",") !== "text") {
            this.violations.push("Notes POST accepts text only; ID and author belong to the service.");
            status = 400;
          } else if (!validNoteText(call.body.text)) status = 400;
          else {
            const note: ActionNote = { id: `e100000000000000${(++this.noteSequence).toString(16).padStart(16, "0")}`, text: call.body.text };
            finding.notes.push(note);
            response = { apiVersion, note };
          }
        }
        if (status >= 400) response = this.failure(status);
        // Commit/snapshot before a held ACK: browser abort is not a server rollback.
        await this.deliver(route, call, status, response, scheduled?.gate);
        return;
      }
      this.violations.push(`Undeclared finding endpoint: ${method} ${path}. No members-directory or reporting calls are needed here.`);
      await this.deliver(route, call, 404, this.failure(404));
    });
  }
}

export const test = base.extend<{ actions: FindingActionsAPI }>({
  actions: async ({ page, baseURL }, use, testInfo) => {
    if (!baseURL) throw new Error("Finding actions require the existing loopback browser configuration.");
    const actions = new FindingActionsAPI(), errors: string[] = [], consoleText: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => consoleText.push(message.text()));
    await actions.install(page, new URL(baseURL).origin);
    try { await use(actions); } finally {
      await actions.releaseResponses();
      expect.soft(actions.violations, "Only declared same-origin finding HTTP operations, unchanged credentials/auth guards and a combined 60-call budget.").toEqual([]);
      expect.soft(errors, "The real UI must not execute note markup or raise unhandled exceptions.").toEqual([]);
      if (!page.isClosed() && page.url().startsWith(baseURL)) {
        const snapshot = await page.evaluate(() => ({
          visible: document.body.innerText, cookie: document.cookie,
          storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
        }));
        for (const [key, value] of snapshot.storage) {
          expect.soft(key, "No finding, note, report, owner or credential values belong in browser storage.").toBe("aspm.theme");
          expect.soft(["light", "dark"]).toContain(value);
        }
        for (const secret of secretCanaries) {
          expect.soft(JSON.stringify(snapshot) + page.url() + consoleText.join("\n"),
            "Synthetic credentials must not appear in the DOM, URL, readable cookies, storage or console.").not.toContain(secret);
        }
      }
      await testInfo.attach("finding-actions-http-ledger", {
        body: JSON.stringify({
          requests: actions.requests.map((call) => call.path === "/api/v1/login" ? { ...call, body: { credentials: "redacted synthetic sign-in" } } : call),
          violations: actions.violations, pageErrors: errors,
        }, null, 2),
        contentType: "application/json",
      });
    }
  },
});
export { expect } from "@playwright/test";
