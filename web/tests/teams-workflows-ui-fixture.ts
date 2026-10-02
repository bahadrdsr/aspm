import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request } from "@playwright/test";
import { JiraUIAPI } from "./jira-work-items-ui-fixture";
import type { JiraControl } from "./jira-work-items-ui-fixture";
import type { WorkCall } from "./saved-work-views-fixture";
import { cookie, password } from "./work-search-data";
import { emptySlackNavigation } from "./slack-navigation";
import { emptySourceNavigation } from "./source-navigation";
import { alpha, apiVersion, connectionsPath, nativeCatalog, profile, rotatedURL, workflowURL } from "./teams-workflows-ui-data";

export type TeamsControl = JiraControl;
type Options = NonNullable<Parameters<JiraUIAPI["read"]>[2]>;
type Body = Parameters<JiraUIAPI["write"]>[2];
interface Reply extends TeamsControl { body: Body; payload: unknown; wait: Promise<void>; done: Promise<void>[] }
const nativeID = /^[a-f0-9]{32}$/;
const connectionDetail = /^\/api\/v1\/integrations\/connections\/[a-f0-9]{32}$/;
const deliveryDetail = /^\/api\/v1\/integrations\/deliveries\/[a-f0-9]{32}$/;
const collection = /^\/api\/v1\/findings\/[a-f0-9]{32}\/deliveries$/;
const localPreview = /^\/api\/v1\/findings\/[a-f0-9]{32}\/delivery-previews$/;
const requestID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee";
const responseHeaders = {
  "content-type": "application/json; charset=utf-8", "cache-control": "no-store",
  "x-content-type-options": "nosniff", "referrer-policy": "no-referrer", "x-request-id": requestID,
};
function failure(status: number, message = "The Teams operation could not be completed") {
  return { apiVersion, error: { code: ({ 400: "invalid-input", 401: "unauthorized", 403: "forbidden",
    404: "not-found", 409: "conflict", 503: "unavailable" } as Record<number, string>)[status],
    message, requestId: requestID, retryable: false } };
}

export class TeamsUIAPI extends JiraUIAPI {
  readonly teamsCalls: WorkCall[] = [];
  readonly teamsDeclarations: TeamsControl[] = [];
  readonly observedHeaders = new Map<WorkCall, Record<string, string>>();
  private readonly teamReplies: Reply[] = [];
  private readonly teamRequests = new Map<Request, WorkCall>();
  private readonly secretParts = new Set([cookie, password]);

  constructor() { super(); this.watchSecret(workflowURL); this.watchSecret(rotatedURL); }
  watchSecret(value: string) {
    this.secretParts.add(value);
    const match = /^https:\/\/[^/]+(\/[^?#]*)(?:\?([^#]*))?/.exec(value);
    for (const part of [match?.[1], ...(match?.[2]?.split("&").map((item) => item.slice(item.indexOf("=") + 1)) ?? [])]) {
      if (!part || part.length < 8) continue;
      this.secretParts.add(part);
      try { this.secretParts.add(decodeURIComponent(part.replaceAll("+", " "))); } catch { /* Invalid input stays literal. */ }
    }
  }
  integrationsEntry(workspace = alpha.id): JiraControl[] {
    const origin = "http://127.0.0.1:18828";
    const known = new Map([...this.roles.keys()].map((id) =>
      [id, [...this.findings.values()].filter((finding) => finding.workspaceId === id).map((finding) => finding.id)]));
    return [
      this.read("/api/v1/integrations/catalog", nativeCatalog, { workspace, entry: true }),
      this.read(connectionsPath, emptySlackNavigation(new URL(`${connectionsPath}?limit=100`, origin), "GET", workspace, known),
        { workspace, entry: true, query: { limit: "100" } }),
      this.read("/api/v1/sources", emptySourceNavigation(new URL("/api/v1/sources", origin), "GET", workspace, [...this.roles.keys()]),
        { workspace, entry: true }),
    ];
  }
  readTeams(path: string, payload: unknown, options: Options = {}): TeamsControl {
    return this.script("GET", path, {}, payload, options);
  }
  listTeams(path: string, payload: unknown, options: Options & { cursor?: string } = {}): TeamsControl {
    return this.readTeams(path, payload, { ...options,
      query: { profile, limit: "100", ...options.cursor ? { cursor: options.cursor } : {} } });
  }
  writeTeams(method: "POST" | "PATCH", path: string, body: Body, payload: unknown, options: Options = {}): TeamsControl {
    return this.script(method, path, body, payload, options);
  }
  private script(method: string, path: string, body: Body, payload: unknown, options: Options): TeamsControl {
    const { workspace = alpha.id, status = 200, held = false, entry = false, query = {} } = options;
    if (!this.roles.has(workspace) || entry && method !== "GET" ||
      !(path === connectionsPath || connectionDetail.test(path) || collection.test(path) ||
        localPreview.test(path) || deliveryDetail.test(path))) throw new Error("Declare only native scoped Teams HTTP.");
    let release!: () => void;
    const reply: Reply = {
      method, path, workspace, query: structuredClone(query), body, status, calls: [], closed: false, maximum: entry ? 2 : 1,
      payload: structuredClone(typeof status === "number" && status >= 400 ? failure(status, options.errorMessage) : payload),
      wait: new Promise<void>((resolve) => { release = resolve; }), release: () => release(), done: [],
    };
    if (!held) release();
    this.teamReplies.push(reply); this.teamsDeclarations.push(reply);
    return reply;
  }
  override async releaseResponses() {
    for (const reply of this.teamReplies) reply.release();
    await Promise.all([super.releaseResponses(), ...this.teamReplies.flatMap((reply) => reply.done)]);
  }
  redact(value: unknown) {
    return JSON.stringify(value, (key, item: unknown) => ["workflowUrl", "password", "token"].includes(key)
      ? "[redacted synthetic credential]" : typeof item === "string"
        ? [...this.secretParts].reduce((text, part) => text.replaceAll(part, "[redacted synthetic credential]"), item) : item, 2);
  }
  override async assertPrivate(page: Page, cleared = false) {
    await super.assertPrivate(page, cleared);
    if (page.isClosed() || !page.url().startsWith("http://127.0.0.1:")) return;
    const state = await page.evaluate(() => ({
      text: document.body.textContent, storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
      attributes: [...document.querySelectorAll("*")].flatMap((element) => [...element.attributes]
        .filter((attribute) => !(element instanceof HTMLInputElement && element.type === "password" && attribute.name === "value"))
        .map((attribute) => attribute.value)),
      fields: [...document.querySelectorAll("input,textarea")].map((field) => ({
        type: field.getAttribute("type"), value: (field as HTMLInputElement).value,
      })),
    }));
    const visible = JSON.stringify({ ...state, fields: undefined }) + page.url() + this.consoleText.join("\n");
    for (const secret of this.secretParts) {
      for (const representation of [secret, encodeURIComponent(secret), Buffer.from(secret).toString("base64")]) {
        expect(visible, "Signed callback/path/query fragments never belong in public or persistent UI.").not.toContain(representation);
      }
      for (const field of state.fields.filter((field) => field.value.includes(secret))) {
        expect(field.type).toBe("password"); expect(cleared, "Discarded forms must clear even hidden password inputs.").toBe(false);
      }
    }
  }
  override async install(page: Page, origin: string) {
    await super.install(page, origin);
    page.on("response", (response) => {
      const call = this.teamRequests.get(response.request());
      if (call) {
        call.responseStatus = response.status();
        call.responseBeforeFailure = call.failure === null && response.request().failure() === null;
        this.observedHeaders.set(call, response.headers());
      }
    });
    page.on("requestfinished", (request) => { const call = this.teamRequests.get(request); if (call) call.finished = true; });
    page.on("requestfailed", (request) => {
      const call = this.teamRequests.get(request); if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.context().route((url) => url.pathname === connectionsPath || connectionDetail.test(url.pathname) ||
      collection.test(url.pathname) || localPreview.test(url.pathname) || deliveryDetail.test(url.pathname), async (route) => {
      const request = route.request(), url = new URL(request.url());
      const declaredPath = this.teamReplies.some((reply) => reply.path === url.pathname && reply.method === request.method());
      if (!url.searchParams.getAll("profile").includes(profile) &&
        !(declaredPath && (url.pathname !== connectionsPath || request.method() !== "GET"))) {
        await route.fallback(); return;
      }
      const call: WorkCall = {
        method: request.method(), path: url.pathname, workspace: undefined, query: Object.fromEntries(url.searchParams),
        body: {}, status: null, response: null, failure: request.failure()?.errorText ?? null,
        responseStatus: null, responseBeforeFailure: false, finished: false,
      };
      // Register before allHeaders so StrictMode/scope cancellation cannot escape the shared ledger.
      this.requests.push(call); this.teamsCalls.push(call); this.teamRequests.set(request, call);
      let reply: Reply | undefined, done: (() => void) | undefined;
      try {
        if (url.origin !== origin) { this.externalAttempts++; throw new Error("External Teams request blocked."); }
        if (this.requests.length > 60) throw new Error("Combined inherited plus Teams 60 API request cap exceeded.");
        const headers = await request.allHeaders(), workspace = headers["x-aspm-workspace-id"];
        call.workspace = workspace;
        if (["authorization", "api-key", "x-api-key", "x-aspm-bootstrap-token", "if-match", "if-unmodified-since",
          "x-aspm-revision", "idempotency-key", "x-idempotency-key"].some((key) => headers[key]) ||
          Object.entries(headers).some(([key, value]) => key !== "cookie" && [...this.secretParts].some((part) => value.includes(part))) ||
          [...this.secretParts].some((part) => url.href.includes(part) || url.href.includes(encodeURIComponent(part)))) {
          throw new Error("No credential routing or invented authority headers.");
        }
        if (!workspace || !this.roles.has(workspace) ||
          !headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${cookie}`) ||
          !this.requests.some((read) => ["/api/v1/session", "/api/v1/login"].includes(read.path) &&
            read.responseStatus === 200 && read.responseBeforeFailure)) throw new Error("Require the inherited scoped authenticated session.");
        if (url.searchParams.size !== Object.keys(call.query).length) throw new Error("Repeated selectors are forbidden.");
        if (call.method === "GET") {
          if (request.postData() !== null) throw new Error("Metadata GET has no body.");
          if (call.path === connectionsPath || collection.test(call.path)) {
            if (call.query.profile !== profile || call.query.limit !== "100" ||
              Object.keys(call.query).some((key) => !["profile", "limit", "cursor"].includes(key)) ||
              "cursor" in call.query && !nativeID.test(call.query.cursor)) throw new Error("Require exact Teams profile/100/native cursor.");
          } else if (url.search !== "") throw new Error("Individual resources have no query.");
        } else {
          const configuring = call.path === connectionsPath || connectionDetail.test(call.path);
          if (!(call.method === "POST" && (call.path === connectionsPath || collection.test(call.path) || localPreview.test(call.path)) ||
            call.method === "PATCH" && connectionDetail.test(call.path))) throw new Error("No extra or finding mutation.");
          const media = headers["content-type"]?.toLowerCase().split(";").map((value) => value.trim());
          if (url.search !== "" || headers.origin !== origin || media?.[0] !== "application/json" ||
            media.some((value) => value.startsWith("charset=") && value !== "charset=utf-8") ||
            headers["content-encoding"] && headers["content-encoding"] !== "identity" ||
            (request.postDataBuffer()?.length ?? 0) > (configuring ? 32 : 16) * 1024) throw new Error("Require bounded same-origin UTF-8 JSON.");
          const body: unknown = request.postDataJSON();
          if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("Require a JSON object.");
          call.body = body as Record<string, unknown>;
        }
        reply = this.teamReplies.find((value) => !value.closed && value.calls.length < value.maximum &&
          value.method === call.method && value.path === call.path && value.workspace === workspace && isDeepStrictEqual(value.query, call.query));
        if (!reply) throw new Error(`Undeclared Teams HTTP intent: ${call.method} ${call.path}. No automatic read/send fallback.`);
        if (!this.authenticated && reply.status !== 401) throw new Error("No successful fallthrough after session loss.");
        if (typeof reply.body === "function") reply.body(call.body);
        else if (!isDeepStrictEqual(call.body, reply.body)) throw new Error("Request differs from its exact explicit Teams intent.");
        const configuring = call.path === connectionsPath || connectionDetail.test(call.path);
        if (call.method !== "GET" && (configuring ? this.serverRoles.get(workspace) !== "admin" :
          !["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) &&
          ![401, 403, 404].includes(reply.status as number)) throw new Error("Current server role denies this write.");
        reply.calls.push(call); reply.done.push(new Promise<void>((resolve) => { done = resolve; }));
        call.status = reply.status === "lost-ack" ? 202 : reply.status; call.response = structuredClone(reply.payload);
        await reply.wait;
        if (reply.status === 401) this.authenticated = false;
        if (reply.status === "lost-ack") await route.abort("failed");
        else await route.fulfill({ status: reply.status, headers: responseHeaders, contentType: responseHeaders["content-type"], json: call.response });
      } catch (cause) { this.violations.push(String(cause)); await route.abort(); }
      finally { done?.(); }
    });
  }
}

export const test = base.extend<{ teams: TeamsUIAPI }>({
  teams: async ({ page, baseURL }, use, info) => {
    if (!baseURL) throw new Error("Use the real App with the isolated configuration.");
    const api = new TeamsUIAPI();
    await api.install(page, new URL(baseURL).origin);
    try { await use(api); } finally {
      await api.releaseResponses(); api.assertData(); await api.assertPrivate(page);
      expect.soft(api.violations).toEqual([]); expect.soft(api.pageErrors).toEqual([]);
      expect.soft(api.externalAttempts).toBe(0); expect.soft(api.requests.length).toBeLessThanOrEqual(60);
      expect.soft(api.requests.filter((call) => call.method !== "GET" &&
        !["/api/v1/login", "/api/v1/logout"].includes(call.path) && !api.teamsCalls.includes(call))).toEqual([]);
      const root = dirname(fileURLToPath(import.meta.url));
      const witnesses = ["teams-workflows-ui-data.ts", "teams-workflows-ui-fixture.ts", "teams-workflows-ui.spec.ts",
        "reviews\\teams-workflows-ui-v1\\CONTRACT.txt", "reviews\\teams-workflows-ui-v1\\playwright.config.ts",
        "jira-work-items-ui-fixture.ts", "saved-work-views-fixture.ts", "work-search-fixture.ts", "work-pagination-fixture.ts"]
        .map((path) => ({ path, sha256: createHash("sha256").update(readFileSync(join(root, path))).digest("hex") }));
      await info.attach("teams-ui-http-ledger", { contentType: "application/json", body: api.redact({
        boundary: "Real App and declared HTTP metadata only. No PG/native TLS/provider/account qualification.",
        author: "Requested GPT-6 Astra; label is not runtime model attestation.", reached: api.reached,
        combinedAPICalls: api.requests.length, requests: api.requests, sourceWitnesses: witnesses,
        teamsRequestIndexes: api.teamsCalls.map((call) => api.requests.indexOf(call)),
        declarations: [...api.declarations, ...api.teamsDeclarations].map(({ method, path, workspace, query, status, closed, maximum, calls }) => ({
          method, path, workspace, query, status, closed, maximum, requestIndexes: calls.map((call) => api.requests.indexOf(call)),
        })),
        inheritedCohorts: [...api.entryCohorts, ...api.queryCohorts, ...api.denialCohorts, ...api.viewEntries]
          .map((entry) => ({ workspace: entry.workspace, closed: entry.closed,
            requestIndexes: entry.calls.map((call) => api.requests.indexOf(call)) })),
        responseHeaders: [...api.observedHeaders].map(([call, headers]) => ({ requestIndex: api.requests.indexOf(call), headers })),
        violations: api.violations, pageErrors: api.pageErrors, externalAttempts: api.externalAttempts,
      }) });
    }
  },
});
export { expect } from "@playwright/test";
