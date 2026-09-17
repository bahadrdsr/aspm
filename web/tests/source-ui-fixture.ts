import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import { bootstrapToken, password, sessionCookie, wrongPassword } from "./application-fixture";
import { catalogResponse } from "./fixtures";
import {
  apiVersion, betaSource, collectionResult, disabledSource, githubSource, nativeID, pageOf, pageParameters,
  queuedCollection, rawDigest, recordsFor, replacementSourceToken, sourceAlpha, sourceBeta, sourceCookie,
  sourceToken, sourceUpdatedAt, sourceUser, sourcesPath, validRepository, validText, validToken,
} from "./source-ui-data";
import type { CollectionState, SourceRole, UICollection, UIRecord, UISource } from "./source-ui-data";

type Denial = 400 | 401 | 403 | 404 | 409 | 503;
export interface SourceCall {
  method: string; path: string; workspace: string | undefined; query: Record<string, string>; body: Record<string, unknown>;
  status: number | null; response: Record<string, unknown> | null; failure: string | null; rawBytes?: number; rawSHA256?: string;
}
export interface SourceControl {
  call: SourceCall | null; calls: SourceCall[]; requested: Promise<void>; delivered: Promise<void>; release: () => void;
}
interface Gate extends SourceControl { wait: Promise<void>; arrive: () => void; complete: () => void }
interface Reply { status: 200 | 201 | 202 | Denial | "drop-ack"; gate: Gate }
const secrets = [sourceToken, replacementSourceToken, sourceCookie, password, wrongPassword, bootstrapToken, sessionCookie];
const sourceFields = ["name", "repository", "token", "enabled"];

export class SourcesUIAPI {
  authenticated = true;
  encryptionAvailable = true;
  storageAvailable = true;
  readonly roles = new Map<string, SourceRole>([[sourceAlpha.id, "admin"], [sourceBeta.id, "analyst"]]);
  readonly serverRoles = new Map(this.roles);
  readonly sources = new Map<string, UISource>();
  readonly collections = new Map<string, UICollection>();
  readonly records = new Map<string, { row: UIRecord; raw: Buffer; workspace: string }>();
  readonly requests: SourceCall[] = [];
  readonly pages: Array<{ call: SourceCall; items: Array<UISource | UICollection | UIRecord>; total: number; nextCursor: string | null }> = [];
  readonly violations: string[] = [];
  readonly pageErrors: string[] = [];
  readonly consoleText: string[] = [];
  externalAttempts = 0;
  private sourceSequence = 0;
  private collectionSequence = 1000;
  private sessionCompleted = false;
  private readReplies = new Map<string, Reply>();
  private writeReplies = new Map<string, Reply[]>();
  private gates = new Set<Gate>();
  private byRequest = new Map<Request, SourceCall>();
  private intents = new Map<string, { id: string; binding: string }>();
  private tokenDigests = new Map<string, string>();

  constructor() { for (const source of [githubSource, disabledSource, betaSource]) this.seedSource(source); }
  seedSource(source: UISource) {
    if (!this.roles.has(source.workspaceId) || source.profile !== "github-cloud-app" ||
      !validText(source.name, 256) || !validRepository(source.repository)) throw new Error("Invalid synthetic source metadata.");
    this.sources.set(source.id, structuredClone(source));
  }
  seedSources(workspace: string, sources: UISource[]) {
    if (sources.some((source) => source.workspaceId !== workspace)) throw new Error("Source seeds must stay scoped.");
    for (const [id, source] of this.sources) if (source.workspaceId === workspace) this.sources.delete(id);
    for (const source of sources) this.seedSource(source);
  }
  seedCollection(collection: UICollection) {
    if (this.sources.get(collection.sourceId)?.workspaceId !== collection.workspaceId) throw new Error("Collection data must belong to a known source.");
    this.collections.set(collection.id, structuredClone(collection));
    for (const record of recordsFor(collection)) this.records.set(record.row.id, { ...record, workspace: collection.workspaceId });
  }
  publish(id: string, state: CollectionState, count = 3) {
    const prior = this.collections.get(id);
    if (!prior || !["queued", "collecting"].includes(prior.state)) throw new Error("Only synthetic nonterminal response data may advance.");
    const next = collectionResult(prior, state, count);
    this.seedCollection(next);
    return structuredClone(next);
  }
  seedHistory(source: UISource, count: number) {
    if (!Number.isInteger(count) || count < 0 || count > 101) throw new Error("History fixture is bounded to 101 collections.");
    for (let index = 1; index <= count; index++) this.seedCollection(collectionResult(queuedCollection(source, index), "succeeded"));
  }
  calls(method: string, path: string) { return this.requests.filter((call) => call.method === method && call.path === path); }
  private control(held: boolean): Gate {
    let release!: () => void, arrive!: () => void, complete!: () => void;
    const gate: Gate = {
      call: null, calls: [], wait: new Promise<void>((resolve) => { release = resolve; }),
      requested: new Promise<void>((resolve) => { arrive = resolve; }),
      delivered: new Promise<void>((resolve) => { complete = resolve; }),
      release: () => release(), arrive: () => arrive(), complete: () => complete(),
    };
    this.gates.add(gate); if (!held) release();
    return gate;
  }
  queueRead(path: string, status: 200 | Denial = 200, held = false, workspace = sourceAlpha.id, cursor?: string) {
    const gate = this.control(held);
    this.readReplies.set(`${workspace} ${path} ${cursor ?? "*"}`, { status, gate });
    return gate;
  }
  queueWrite(method: "POST" | "PATCH", path: string, status: 200 | 201 | 202 | Denial | "drop-ack", held = false) {
    const gate = this.control(held), key = `${method} ${path}`;
    this.writeReplies.set(key, [...(this.writeReplies.get(key) ?? []), { status, gate }]);
    return gate;
  }
  async releaseResponses() {
    const arrived = [...this.gates].filter((gate) => gate.call !== null);
    for (const gate of this.gates) gate.release();
    await Promise.all(arrived.map((gate) => gate.delivered));
  }
  private error(status: number) {
    const entries: Record<number, [string, string]> = {
      400: ["invalid-input", "The request is invalid"], 401: ["unauthorized", "Authentication is required"],
      403: ["forbidden", "This operation is not permitted"], 404: ["not-found", "The requested resource was not found"],
      409: ["conflict", "The request conflicts with existing state"], 413: ["too-large", "The request exceeds the configured size limit"],
      429: ["rate-limited", "Synthetic combined request budget exceeded"], 503: ["unavailable", "The operation could not be completed"],
    };
    const [code, message] = entries[status];
    return { apiVersion, error: { code, message, requestId: "synthetic-sources-ui-request", retryable: false } };
  }
  private async deliver(route: Route, call: SourceCall, status: number, value: Record<string, unknown> | Buffer, reply?: Reply) {
    call.status = status;
    const bytes = Buffer.isBuffer(value) ? Buffer.from(value) : null;
    call.response = bytes ? null : structuredClone(value) as Record<string, unknown>;
    if (bytes) { call.rawBytes = bytes.length; call.rawSHA256 = rawDigest(bytes); }
    try {
      if (reply) { reply.gate.call = call; reply.gate.calls.push(call); reply.gate.arrive(); await reply.gate.wait; }
      if (status === 401) this.authenticated = false;
      if (reply?.status === "drop-ack" && status < 400) await route.abort("failed");
      else if (bytes) await route.fulfill({ status, contentType: "application/octet-stream", body: bytes });
      else await route.fulfill({ status, json: call.response });
    } finally {
      reply?.gate.complete(); if (reply) this.gates.delete(reply.gate);
    }
  }
  private sourceWrite(call: SourceCall, workspace: string, id?: string): UISource | null {
    const input = call.body, create = id === undefined;
    if (Object.keys(input).some((key) => ![...sourceFields, ...(create ? ["profile"] : [])].includes(key)) ||
      create && (Object.keys(input).length !== 5 || input.profile !== "github-cloud-app") ||
      (create || "name" in input) && !validText(input.name, 256) ||
      (create || "repository" in input) && !validRepository(input.repository) ||
      (create || "token" in input) && !validToken(input.token) ||
      (create || "enabled" in input) && typeof input.enabled !== "boolean") return null;
    if (create) {
      const source: UISource = {
        id: nativeID("c3", ++this.sourceSequence), workspaceId: workspace, profile: "github-cloud-app",
        name: String(input.name), repository: String(input.repository), enabled: input.enabled === true,
        credentialConfigured: true, revision: 1, createdAt: sourceUpdatedAt, updatedAt: sourceUpdatedAt,
      };
      this.tokenDigests.set(source.id, rawDigest(Buffer.from(String(input.token))));
      return source;
    }
    const prior = this.sources.get(id);
    if (!prior || prior.workspaceId !== workspace) return null;
    const next = structuredClone(prior);
    if (typeof input.name === "string") next.name = input.name;
    if (typeof input.repository === "string") next.repository = input.repository;
    if (typeof input.enabled === "boolean") next.enabled = input.enabled;
    const tokenChanged = typeof input.token === "string" && this.tokenDigests.get(id) !== rawDigest(Buffer.from(input.token));
    if (tokenChanged) this.tokenDigests.set(id, rawDigest(Buffer.from(String(input.token))));
    if (next.name !== prior.name || next.repository !== prior.repository || next.enabled !== prior.enabled || tokenChanged) {
      next.revision++; next.updatedAt = sourceUpdatedAt;
    }
    return next;
  }
  async assertPrivate(page: Page, cleared = false) {
    if (page.isClosed()) return;
    const state = await page.evaluate(() => ({
      text: document.body.textContent, cookie: document.cookie,
      storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
      fields: [...document.querySelectorAll("input,textarea")].map((element) => ({ type: element.getAttribute("type"), value: (element as HTMLInputElement).value })),
    }));
    const publicState = JSON.stringify({ ...state, fields: undefined }) + page.url() + this.consoleText.join("\n");
    for (const secret of secrets) for (const encoded of [secret, encodeURIComponent(secret), Buffer.from(secret).toString("base64")]) {
      expect(publicState, "Credentials must not enter UI text, URL, readable cookies, storage or console.").not.toContain(encoded);
    }
    for (const field of state.fields.filter((field) => [sourceToken, replacementSourceToken].includes(field.value))) {
      expect(field.type).toBe("password");
      expect(cleared, "Close/success/scope/session loss must clear every installation-bearer field.").toBe(false);
    }
    for (const [key, value] of state.storage) { expect(key).toBe("aspm.theme"); expect(["light", "dark"]).toContain(value); }
  }
  async install(page: Page, origin: string) {
    const baseURL = new URL(origin);
    if (baseURL.protocol !== "http:" || baseURL.hostname !== "127.0.0.1") throw new Error("Only the existing loopback browser-test boundary is authorized.");
    await page.context().addCookies([{ name: "aspm_session", value: sourceCookie, url: origin, httpOnly: true, sameSite: "Lax" }]);
    page.on("pageerror", (error) => this.pageErrors.push(error.message));
    page.on("console", (message) => this.consoleText.push(message.text()));
    page.on("requestfailed", (request) => { const call = this.byRequest.get(request); if (call) call.failure = request.failure()?.errorText ?? "request failed"; });
    await page.route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) {
        this.externalAttempts++; this.violations.push("No GitHub/native/provider/rawURL/OAuth/evidence URL traffic is authorized.");
        await route.abort(); return;
      }
      if (!url.pathname.startsWith("/api/")) { await route.continue(); return; }
      const headers = await request.allHeaders(), method = request.method(), path = url.pathname;
      const call: SourceCall = { method, path, workspace: headers["x-aspm-workspace-id"], query: Object.fromEntries(url.searchParams), body: {}, status: null, response: null, failure: null };
      this.requests.push(call); this.byRequest.set(request, call);
      if (this.requests.length > 60) {
        this.violations.push("The original combined 60-API-request per-case budget was exceeded.");
        await this.deliver(route, call, 429, this.error(429)); return;
      }
      if (headers.authorization || headers["x-aspm-bootstrap-token"] ||
        secrets.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret))) ||
        Object.entries(headers).some(([key, value]) => key !== "cookie" && [sourceToken, replacementSourceToken].some((token) => value.includes(token)))) {
        this.violations.push("Use the HttpOnly application session, not bearer/bootstrap/provider/URL/header authority.");
      }
      if (headers["if-match"] || headers["x-aspm-revision"]) this.violations.push("Returned source revision is not an invented conditional-write header.");
      const sourceID = /^\/api\/v1\/sources\/([a-f0-9]{32})$/.exec(path)?.[1];
      const sourceCollections = /^\/api\/v1\/sources\/([a-f0-9]{32})\/collections$/.exec(path)?.[1];
      const collectionID = /^\/api\/v1\/sources\/collections\/([a-f0-9]{32})$/.exec(path)?.[1];
      const recordCollection = /^\/api\/v1\/sources\/collections\/([a-f0-9]{32})\/records$/.exec(path)?.[1];
      const evidence = /^\/api\/v1\/sources\/collections\/([a-f0-9]{32})\/records\/([a-f0-9]{32})\/evidence$/.exec(path);
      const write = method === "PATCH" && sourceID !== undefined ||
        method === "POST" && (sourceCollections !== undefined || [sourcesPath, "/api/v1/login", "/api/v1/logout"].includes(path));
      if (method !== "GET" && !write || write && url.search !== "") {
        this.violations.push(`Undeclared Sources operation: ${method} ${path}. No dispatch/retry/normalization/asset mutation is permitted.`);
        await route.abort(); return;
      }
      if (write) {
        if (headers.origin !== origin) this.violations.push("Writes require the matching Origin.");
        if (path !== "/api/v1/logout") {
          let parsed: unknown; try { parsed = request.postDataJSON(); } catch { parsed = null; }
          const media = headers["content-type"]?.split(";").map((part) => part.trim().toLowerCase());
          if (media?.[0] !== "application/json" || media.some((part) => part.startsWith("charset=") && part !== "charset=utf-8") ||
            headers["content-encoding"] && headers["content-encoding"] !== "identity" ||
            parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
            this.violations.push("Sources use bounded UTF-8 JSON objects, never multipart or encoded native payloads.");
            await this.deliver(route, call, 400, this.error(400)); return;
          }
          call.body = parsed as Record<string, unknown>;
          const allowed = sourceCollections ? ["idempotencyKey"] : sourceID ? sourceFields :
            path === sourcesPath ? [...sourceFields, "profile"] : ["email", "password"];
          if (Object.keys(call.body).some((key) => !allowed.includes(key))) this.violations.push("Unknown endpoint/header/key/actor/approval fields remain forbidden even on denied writes.");
          if ((request.postDataBuffer()?.length ?? 0) > (sourceCollections ? 16 << 10 : 32 << 10)) {
            this.violations.push("Native source/collection JSON size limit exceeded.");
            await this.deliver(route, call, 413, this.error(413)); return;
          }
        }
      }
      const cookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${sourceCookie}`);
      const session = () => ({ apiVersion, user: sourceUser, expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        workspaces: [sourceAlpha, sourceBeta].map((workspace) => ({ ...workspace, role: this.roles.get(workspace.id)! })) });
      if (method === "GET" && path === "/api/v1/session" && url.search === "") {
        if (this.authenticated && cookie) { await this.deliver(route, call, 200, session()); this.sessionCompleted = true; }
        else await this.deliver(route, call, 401, this.error(401));
        return;
      }
      if (method === "POST" && path === "/api/v1/login") {
        if (!isDeepStrictEqual(call.body, { email: sourceUser.email, password })) await this.deliver(route, call, 401, this.error(401));
        else {
          this.authenticated = true; call.status = 200; call.response = session();
          await route.fulfill({ json: call.response, headers: { "set-cookie": `aspm_session=${sourceCookie}; Path=/; HttpOnly; SameSite=Lax` } });
          this.sessionCompleted = true;
        }
        return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected Sources traffic began before session verification completed.");
      if (!this.authenticated || !cookie) { await this.deliver(route, call, 401, this.error(401)); return; }
      if (method === "POST" && path === "/api/v1/logout") {
        this.authenticated = false; this.sessionCompleted = false; call.status = 204;
        await route.fulfill({ status: 204, headers: { "set-cookie": "aspm_session=; Max-Age=0; Path=/; HttpOnly; SameSite=Lax" } }); return;
      }
      const workspace = call.workspace;
      if (!workspace || !this.roles.has(workspace)) {
        this.violations.push("Sources require the selected session-owned workspace.");
        await this.deliver(route, call, 403, this.error(403)); return;
      }
      const reply = method === "GET" ? this.readReplies.get(`${workspace} ${path} ${url.searchParams.get("cursor") ?? ""}`) ??
        this.readReplies.get(`${workspace} ${path} *`) : this.writeReplies.get(`${method} ${path}`)?.shift();
      if (!this.serverRoles.has(workspace)) { await this.deliver(route, call, 403, this.error(403), reply); return; }
      const readStatus = typeof reply?.status === "number" ? reply.status : 200;
      try {
        if (method === "GET" && (path === sourcesPath || sourceCollections || recordCollection)) {
          pageParameters(url);
          if (sourceCollections && this.sources.get(sourceCollections)?.workspaceId !== workspace ||
            recordCollection && this.collections.get(recordCollection)?.workspaceId !== workspace) {
            await this.deliver(route, call, 404, this.error(404), reply); return;
          }
          const values = path === sourcesPath ? [...this.sources.values()].filter((source) => source.workspaceId === workspace) :
            sourceCollections ? [...this.collections.values()].filter((collection) => collection.workspaceId === workspace && collection.sourceId === sourceCollections) :
              [...this.records.values()].filter((record) => record.workspace === workspace && record.row.collectionId === recordCollection).map((record) => record.row);
          const result = pageOf<UISource | UICollection | UIRecord>(values, url);
          if (readStatus === 200) this.pages.push({ call, ...structuredClone(result) });
          await this.deliver(route, call, readStatus, readStatus === 200 ? { ...result } : this.error(readStatus), reply);
        } else if (method === "GET" && (sourceID || collectionID || evidence)) {
          if (url.search !== "") throw new Error("Source/collection/evidence detail has no query parameters.");
          const value = sourceID ? this.sources.get(sourceID) : collectionID ? this.collections.get(collectionID) : this.records.get(evidence![2]);
          const valid = value && ("workspaceId" in value ? value.workspaceId === workspace : value.workspace === workspace &&
            value.row.collectionId === evidence![1]);
          if (!valid) { await this.deliver(route, call, 404, this.error(404), reply); return; }
          if (readStatus !== 200) { await this.deliver(route, call, readStatus, this.error(readStatus), reply); return; }
          if (evidence) {
            if (!this.storageAvailable) { await this.deliver(route, call, 503, this.error(503), reply); return; }
            await this.deliver(route, call, 200, this.records.get(evidence[2])!.raw, reply);
          } else await this.deliver(route, call, 200, { apiVersion, [sourceID ? "source" : "collection"]: value }, reply);
        } else if (write && (path === sourcesPath || sourceID)) {
          let status = typeof reply?.status === "number" ? reply.status : sourceID ? 200 : 201;
          if (this.serverRoles.get(workspace) !== "admin") status = 403;
          else if (sourceID && this.sources.get(sourceID)?.workspaceId !== workspace) status = 404;
          else if (!this.encryptionAvailable && (!sourceID || "token" in call.body)) status = 503;
          if (status >= 400) { await this.deliver(route, call, status, this.error(status), reply); return; }
          const value = this.sourceWrite(call, workspace, sourceID);
          if (!value) {
            this.violations.push("Source create/PATCH must use only valid selected profile/name/repository/token/enabled fields.");
            await this.deliver(route, call, 400, this.error(400), reply); return;
          }
          this.seedSource(value); await this.deliver(route, call, status, { apiVersion, source: value }, reply);
        } else if (method === "POST" && sourceCollections) {
          let status = typeof reply?.status === "number" ? reply.status : 202;
          const source = this.sources.get(sourceCollections);
          if (Object.keys(call.body).join(",") !== "idempotencyKey" || !validText(call.body.idempotencyKey, 256)) {
            this.violations.push("Collection confirmation sends ONLY a generated bounded idempotencyKey."); status = 400;
          } else if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) status = 403;
          else if (!source || source.workspaceId !== workspace) status = 404;
          else if (!source.enabled) status = 409;
          else if (!this.storageAvailable) status = 503;
          if (status >= 400 || !source) { await this.deliver(route, call, status, this.error(status), reply); return; }
          const key = `${workspace}:${String(call.body.idempotencyKey)}`;
          const binding = JSON.stringify({ workspace, source: source.id, revision: source.revision, repository: source.repository, profile: source.profile, actor: sourceUser.id });
          const prior = this.intents.get(key);
          if (prior && prior.binding !== binding) { await this.deliver(route, call, 409, this.error(409), reply); return; }
          const value = prior ? this.collections.get(prior.id)! : queuedCollection(source, ++this.collectionSequence);
          if (!prior) { this.seedCollection(value); this.intents.set(key, { id: value.id, binding }); }
          await this.deliver(route, call, prior ? 200 : 202, { apiVersion, collection: value }, reply);
        } else if (method === "GET" && path === "/api/v1/integrations/catalog" && url.search === "") {
          await this.deliver(route, call, 200, { ...catalogResponse });
        } else if (method === "GET" && path === "/api/v1/integrations/connections") {
          pageParameters(url); await this.deliver(route, call, 200, { apiVersion, items: [], total: 0, nextCursor: null });
        } else if (method === "GET" && ["/api/v1/assets", "/api/v1/work"].includes(path) && url.search === "") {
          await this.deliver(route, call, 200, { apiVersion, dataOrigin: "synthetic", items: [], total: 0, nextCursor: null });
        } else {
          this.violations.push(`Undeclared Sources UI endpoint: ${method} ${path}.`);
          await this.deliver(route, call, 404, this.error(404), reply);
        }
      } catch (error) {
        this.violations.push(String(error)); reply?.gate.complete(); throw error;
      }
    });
  }
}

export const test = base.extend<{ sources: SourcesUIAPI }>({
  sources: async ({ page, baseURL }, use, testInfo) => {
    if (!baseURL) throw new Error("Sources tests require the unchanged loopback browser configuration.");
    const sources = new SourcesUIAPI(); await sources.install(page, new URL(baseURL).origin);
    try { await use(sources); } finally {
      await sources.releaseResponses();
      expect.soft(sources.violations, "Only native declared same-origin source/collection/evidence HTTP, unchanged credentials/Origin and 60-call budget.").toEqual([]);
      expect.soft(sources.pageErrors).toEqual([]);
      await sources.assertPrivate(page);
      await testInfo.attach("source-collection-ui-http-ledger", {
        body: JSON.stringify({
          requests: sources.requests.map((call) => ({
            ...call, body: call.path === "/api/v1/login" ? { credentials: "redacted synthetic login" } :
              "token" in call.body ? { ...call.body, token: "redacted synthetic installation bearer" } : call.body,
          })),
          violations: sources.violations, pageErrors: sources.pageErrors, externalAttempts: sources.externalAttempts,
          boundary: "Synthetic HTTP metadata/raw response bytes only; no actual core/worker/parser/GitHub execution or application state replacement.",
        }, null, 2), contentType: "application/json",
      });
    }
  },
});
export { expect } from "@playwright/test";
