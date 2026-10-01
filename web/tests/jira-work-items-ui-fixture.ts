import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request } from "@playwright/test";
import { SavedViewsAPI } from "./saved-work-views-fixture";
import type { WorkCall } from "./saved-work-views-fixture";
import { cookie, password } from "./work-search-data";
import { catalogResponse } from "./fixtures";
import { emptySlackNavigation } from "./slack-navigation";
import { emptySourceNavigation } from "./source-navigation";
import {
  alpha, apiVersion, beta, connectionsPath, draftToken, findingPath, first, profile, replacementToken,
} from "./jira-work-items-ui-data";

type Status = 200 | 201 | 202 | 400 | 401 | 403 | 404 | 409 | 503 | "lost-ack";
type Body = object | ((body: Record<string, unknown>) => void);
interface Options {
  status?: Status; workspace?: string; held?: boolean; entry?: boolean; query?: Record<string, string>; errorMessage?: string;
}
export interface JiraControl {
  method: string; path: string; workspace: string; query: Record<string, string>;
  calls: WorkCall[]; status: Status; closed: boolean; maximum: number; release: () => void;
}
interface Reply extends JiraControl { body: Body; payload: unknown; wait: Promise<void>; done: Promise<void>[] }
const secrets = [draftToken, replacementToken, cookie, password];
const nativeID = /^[a-f0-9]{32}$/;
const collectionPath = /^\/api\/v1\/findings\/[a-f0-9]{32}\/deliveries$/;
const localPreviewPath = /^\/api\/v1\/findings\/[a-f0-9]{32}\/delivery-previews$/;
const connectionDetailPath = /^\/api\/v1\/integrations\/connections\/[a-f0-9]{32}$/;
const deliveryDetailPath = /^\/api\/v1\/integrations\/deliveries\/[a-f0-9]{32}$/;
const findingDetailPath = /^\/api\/v1\/findings\/[a-f0-9]{32}$/;
function errorBody(status: number, message?: string) {
  return { apiVersion, error: {
    code: ({ 400: "invalid-input", 401: "unauthorized", 403: "forbidden", 404: "not-found",
      409: "conflict", 503: "unavailable" } as Record<number, string>)[status],
    message: message ?? "Synthetic Jira operation unavailable or denied.",
    requestId: "synthetic-jira-ui-request", retryable: status === 503,
  } };
}
function witnesses() {
  const root = dirname(fileURLToPath(import.meta.url));
  return ["jira-work-items-ui-data.ts", "jira-work-items-ui-fixture.ts", "jira-work-items-ui.spec.ts",
    "reviews\\jira-work-items-ui-v1\\CONTRACT.txt", "reviews\\jira-work-items-ui-v1\\playwright.config.ts",
    "saved-work-views-fixture.ts", "work-search-fixture.ts", "work-pagination-fixture.ts",
    "slack-navigation.ts", "source-navigation.ts"].map((path) => {
    const bytes = readFileSync(join(root, path));
    return { path: `web\\tests\\${path}`, bytes: bytes.length, sha256: createHash("sha256").update(bytes).digest("hex") };
  });
}

export class JiraUIAPI extends SavedViewsAPI {
  readonly jiraCalls: WorkCall[] = [];
  readonly declarations: JiraControl[] = [];
  private readonly scripts: Reply[] = [];
  private readonly jiraRequests = new Map<Request, WorkCall>();

  constructor() {
    super();
    this.roles.set(alpha.id, "admin"); this.serverRoles.set(alpha.id, "admin");
    this.roles.set(beta.id, "analyst"); this.serverRoles.set(beta.id, "analyst");
  }
  read(path: string, payload: unknown, options: Options = {}): JiraControl {
    return this.declare("GET", path, {}, payload, options);
  }
  write(method: "POST" | "PATCH", path: string, body: Body, payload: unknown, options: Options = {}): JiraControl {
    return this.declare(method, path, body, payload, options);
  }
  list(path: string, payload: unknown, options: Options & { cursor?: string } = {}): JiraControl {
    const query = { profile, limit: "100", ...options.cursor ? { cursor: options.cursor } : {} };
    return this.read(path, payload, { ...options, query });
  }
  findingEntry(id = first.id, options: Options = {}) {
    const finding = this.findings.get(id);
    if (!finding) throw new Error("Declare only existing scoped finding metadata.");
    return this.read(findingPath(id), { apiVersion, dataOrigin: "synthetic", finding },
      { entry: true, workspace: finding.workspaceId, ...options });
  }
  navigationEntry(workspace = alpha.id): JiraControl[] {
    const origin = "http://127.0.0.1:18827";
    const known = new Map([...this.roles.keys()].map((id) =>
      [id, [...this.findings.values()].filter((finding) => finding.workspaceId === id).map((finding) => finding.id)]));
    const slackURL = new URL(`${connectionsPath}?limit=100`, origin);
    const sourceURL = new URL("/api/v1/sources", origin);
    return [
      this.read("/api/v1/integrations/catalog", catalogResponse, { workspace, entry: true }),
      this.read(connectionsPath, emptySlackNavigation(slackURL, "GET", workspace, known),
        { workspace, entry: true, query: { limit: "100" } }),
      this.read("/api/v1/sources", emptySourceNavigation(sourceURL, "GET", workspace, [...this.roles.keys()]),
        { workspace, entry: true }),
    ];
  }
  private declare(method: string, path: string, body: Body, payload: unknown, options: Options): JiraControl {
    const { workspace = alpha.id, status = 200, held = false, entry = false, query = {} } = options;
    if (!this.roles.has(workspace) || entry && method !== "GET" || !path.startsWith("/api/v1/")) {
      throw new Error("Only declared native metadata paths and bounded GET cohorts are permitted.");
    }
    let release!: () => void;
    const reply: Reply = {
      method, path, workspace, query: structuredClone(query), body, status, calls: [], closed: false,
      maximum: entry ? 2 : 1, payload: structuredClone(typeof status === "number" && status >= 400
        ? errorBody(status, options.errorMessage) : payload),
      wait: new Promise<void>((resolve) => { release = resolve; }), release: () => release(), done: [],
    };
    if (!held) release();
    this.scripts.push(reply); this.declarations.push(reply);
    return reply;
  }
  finish(control: JiraControl) {
    expect(control.closed).toBe(false);
    expect(control.calls.length).toBeGreaterThan(0); expect(control.calls.length).toBeLessThanOrEqual(control.maximum);
    if (control.status === "lost-ack") {
      expect(control.calls).toHaveLength(1);
      expect(control.calls[0].failure).not.toBeNull(); expect(control.calls[0].responseStatus).toBeNull();
    } else {
      expect(control.calls.some((call) => call.responseStatus === control.status && call.responseBeforeFailure &&
        (control.status === 401 || call.finished && call.failure === null)),
      "An actual response event must precede any auth abort; fulfillment alone is not evidence.").toBe(true);
    }
    for (const call of control.calls) {
      expect(call).toMatchObject({ method: control.method, path: control.path, workspace: control.workspace, query: control.query });
      if (call.failure !== null && control.status !== "lost-ack") expect(call.failure).toMatch(/abort/i);
    }
    control.closed = true;
  }
  omitUnrequested(control: JiraControl) {
    expect(control.calls).toHaveLength(0);
    control.closed = true;
  }
  finishAborted(control: JiraControl) {
    expect(control.calls.length).toBeGreaterThan(0);
    expect(control.calls.length).toBeLessThanOrEqual(control.maximum);
    for (const call of control.calls) {
      expect(call.failure, "A held old-scope request must actually be aborted.").toMatch(/abort/i);
      expect(call.responseStatus).toBeNull();
    }
    control.release(); control.closed = true;
  }
  override async releaseResponses() {
    for (const reply of this.scripts) reply.release();
    await Promise.all([super.releaseResponses(), ...this.scripts.flatMap((reply) => reply.done)]);
  }
  override async assertPrivate(page: Page, cleared = false) {
    await super.assertPrivate(page);
    if (page.isClosed() || !page.url().startsWith("http://127.0.0.1:")) return;
    const state = await page.evaluate(() => ({
      text: document.body.textContent, cookie: document.cookie, storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
      attributes: [...document.querySelectorAll("*")].flatMap((element) => [...element.attributes]
        .filter((attribute) => !(element instanceof HTMLInputElement && element.type === "password" && attribute.name === "value"))
        .map((attribute) => attribute.value)),
      fields: [...document.querySelectorAll("input,textarea")].map((element) => ({
        type: element.getAttribute("type"), value: (element as HTMLInputElement).value,
      })),
    }));
    const publicState = JSON.stringify({ ...state, fields: undefined }) + page.url() + this.consoleText.join("\n");
    for (const secret of secrets) for (const value of [secret, encodeURIComponent(secret), Buffer.from(secret).toString("base64")]) {
      expect(publicState, "Credentials never belong in displayed text, attributes, URL, storage or console.").not.toContain(value);
    }
    for (const field of state.fields.filter((field) => [draftToken, replacementToken].includes(field.value))) {
      expect(field.type).toBe("password");
      expect(cleared, "Closed/successful/invalidated forms must remove secret drafts, including hidden inputs.").toBe(false);
    }
  }
  override async install(page: Page, origin: string) {
    await super.install(page, origin);
    page.on("response", (response) => {
      const call = this.jiraRequests.get(response.request());
      if (call) {
        call.responseStatus = response.status();
        call.responseBeforeFailure = call.failure === null && response.request().failure() === null;
      }
    });
    page.on("requestfinished", (request) => { const call = this.jiraRequests.get(request); if (call) call.finished = true; });
    page.on("requestfailed", (request) => {
      const call = this.jiraRequests.get(request); if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.context().route((url) => url.pathname.startsWith("/api/v1/integrations/") ||
      url.pathname === "/api/v1/sources" || findingDetailPath.test(url.pathname) ||
      collectionPath.test(url.pathname) || localPreviewPath.test(url.pathname), async (route) => {
      const request = route.request(), url = new URL(request.url());
      const call: WorkCall = {
        method: request.method(), path: url.pathname, workspace: undefined, query: Object.fromEntries(url.searchParams),
        body: {}, status: null, response: null, failure: request.failure()?.errorText ?? null,
        responseStatus: null, responseBeforeFailure: false, finished: false,
      };
      // A StrictMode cancellation can happen during allHeaders; ledger registration must precede it.
      this.requests.push(call); this.jiraRequests.set(request, call);
      const jira = localPreviewPath.test(call.path) || connectionDetailPath.test(call.path) || deliveryDetailPath.test(call.path) ||
        (call.path === connectionsPath || collectionPath.test(call.path)) && (call.method !== "GET" || url.searchParams.has("profile"));
      if (jira) this.jiraCalls.push(call);
      let reply: Reply | undefined, done: (() => void) | undefined;
      try {
        if (url.origin !== origin) { this.externalAttempts++; throw new Error("External/provider traffic is forbidden."); }
        if (this.requests.length > 60) throw new Error("Combined inherited plus Jira 60 API request cap exceeded.");
        const headers = await request.allHeaders();
        call.workspace = headers["x-aspm-workspace-id"];
        if (["authorization", "api-key", "x-api-key", "x-aspm-bootstrap-token", "if-match", "if-unmodified-since",
          "x-aspm-revision", "idempotency-key", "x-idempotency-key"].some((key) => headers[key]) ||
          Object.entries(headers).some(([key, value]) => key !== "cookie" && secrets.some((secret) => value.includes(secret))) ||
          secrets.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret)))) {
          throw new Error("Only cookie/workspace/Origin authority is allowed, no credential routing or invented revision headers.");
        }
        const workspace = call.workspace;
        const hasCookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${cookie}`);
        if (!workspace || !this.roles.has(workspace) || !hasCookie ||
          !this.requests.some((read) => ["/api/v1/session", "/api/v1/login"].includes(read.path) &&
            read.responseStatus === 200 && read.responseBeforeFailure)) throw new Error("Metadata needs a current scoped application session.");
        if (url.searchParams.size !== Object.keys(call.query).length) throw new Error("Repeated query selectors are forbidden.");
        if (call.method === "GET") {
          if (request.postData() !== null) throw new Error("GET metadata must not carry a body.");
          if (jira && (call.path === connectionsPath || collectionPath.test(call.path)) &&
            (call.query.profile !== profile || call.query.limit !== "100" ||
              Object.keys(call.query).some((key) => !["profile", "limit", "cursor"].includes(key)) ||
              "cursor" in call.query && !nativeID.test(call.query.cursor))) throw new Error("Use exact Jira profile and native 100/cursor list query.");
          if ((connectionDetailPath.test(call.path) || deliveryDetailPath.test(call.path)) && url.search !== "") {
            throw new Error("Individual resources have no profile or invented query.");
          }
        } else {
          if (!(call.method === "POST" && (call.path === connectionsPath || collectionPath.test(call.path) || localPreviewPath.test(call.path)) ||
            call.method === "PATCH" && connectionDetailPath.test(call.path))) throw new Error("No finding/provider mutation or extra Jira operation is declared.");
          const media = headers["content-type"]?.split(";").map((value) => value.trim().toLowerCase());
          const limit = call.path === connectionsPath || connectionDetailPath.test(call.path) ? 32 << 10 : 16 << 10;
          if (url.search !== "" || headers.origin !== origin || media?.[0] !== "application/json" ||
            media.some((value) => value.startsWith("charset=") && value !== "charset=utf-8") ||
            headers["content-encoding"] && headers["content-encoding"] !== "identity" ||
            (request.postDataBuffer()?.length ?? 0) > limit) throw new Error("Writes require exact bounded same-origin UTF-8 JSON.");
          const body: unknown = request.postDataJSON();
          if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("JSON must be an object.");
          call.body = body as Record<string, unknown>;
        }
        reply = this.scripts.find((value) => !value.closed && value.calls.length < value.maximum &&
          value.method === call.method && value.path === call.path && value.workspace === workspace && isDeepStrictEqual(value.query, call.query));
        if (!reply) throw new Error(`Undeclared HTTP intent: ${call.method} ${call.path}. No read fallback or automatic retry exists.`);
        if (!this.authenticated && reply.status !== 401) throw new Error("No aborted-401 fallthrough to successful metadata.");
        if (typeof reply.body === "function") reply.body(call.body);
        else if (!isDeepStrictEqual(reply.body, call.body)) throw new Error("Body differed from the exact declared explicit intent.");
        const configuring = call.method !== "GET" && (call.path === connectionsPath || connectionDetailPath.test(call.path));
        const writing = call.method !== "GET";
        if (writing && (configuring ? this.serverRoles.get(workspace) !== "admin" :
          !["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) &&
          ![401, 403, 404].includes(reply.status as number)) throw new Error("Current server role denies this write.");
        reply.calls.push(call); reply.done.push(new Promise<void>((resolve) => { done = resolve; }));
        call.status = reply.status === "lost-ack" ? 202 : reply.status; call.response = structuredClone(reply.payload);
        await reply.wait;
        if (reply.status === 401) this.authenticated = false;
        if (reply.status === "lost-ack") await route.abort("failed");
        else await route.fulfill({ status: reply.status, json: call.response });
      } catch (cause) { this.violations.push(String(cause)); await route.abort(); }
      finally { done?.(); }
    });
  }
}

export const test = base.extend<{ jira: JiraUIAPI }>({
  jira: async ({ page, baseURL }, use, info) => {
    if (!baseURL) throw new Error("Use the local real-App browser server.");
    const api = new JiraUIAPI();
    await api.install(page, new URL(baseURL).origin);
    try { await use(api); } finally {
      await api.releaseResponses(); api.assertData(); await api.assertPrivate(page);
      expect.soft(api.violations).toEqual([]); expect.soft(api.pageErrors).toEqual([]);
      expect.soft(api.externalAttempts).toBe(0); expect.soft(api.requests.length).toBeLessThanOrEqual(60);
      expect.soft(api.requests.filter((call) => call.method !== "GET" &&
        !["/api/v1/login", "/api/v1/logout"].includes(call.path) && !api.jiraCalls.includes(call))).toEqual([]);
      const body = JSON.stringify({
        boundary: "Declared synthetic metadata at real App HTTP only. No backend, worker, provider or account qualification.",
        requestedAuthorLane: "GPT-6 Astra (label, not runtime attestation)", reached: api.reached,
        requests: api.requests, jiraRequestIndexes: api.jiraCalls.map((call) => api.requests.indexOf(call)),
        declarations: api.declarations.map(({ method, path, workspace, query, calls, status, closed, maximum }) => ({
          method, path, workspace, query, status, closed, maximum, requestIndexes: calls.map((call) => api.requests.indexOf(call)),
        })),
        violations: api.violations, pageErrors: api.pageErrors, externalAttempts: api.externalAttempts,
        combinedAPICalls: api.requests.length, sourceWitnesses: witnesses(),
      }, (key, value: unknown) => key === "token" || key === "password" ? "[redacted synthetic credential]" :
        typeof value === "string" ? secrets.reduce((text, secret) => text.replaceAll(secret, "[redacted synthetic credential]"), value) : value, 2);
      await info.attach("jira-ui-http-ledger", { contentType: "application/json", body });
    }
  },
});
export { expect } from "@playwright/test";
