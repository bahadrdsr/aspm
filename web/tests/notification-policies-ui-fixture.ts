import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request } from "@playwright/test";
import { JiraUIAPI } from "./jira-work-items-ui-fixture";
import type { WorkCall } from "./saved-work-views-fixture";
import { cookie } from "./work-search-data";
import { alpha, apiVersion, policiesPath } from "./notification-policies-ui-data";

type Status = 200 | 201 | 400 | 401 | 403 | 404 | 409;
type Body = Record<string, unknown>;
interface Options {
  status?: Status; workspace?: string; held?: boolean; query?: Record<string, string>;
}
export interface PolicyControl {
  method: string; path: string; workspace: string; query: Record<string, string>;
  calls: WorkCall[]; status: Status; closed: boolean; maximum: number; release: () => void;
}
interface Reply extends PolicyControl {
  body: Body; payload: unknown; wait: Promise<void>; done: Promise<void>[];
}
const policyDetail = /^\/api\/v1\/integrations\/notification-policies\/[a-f0-9]{32}$/;
const policyEvents = /^\/api\/v1\/integrations\/notification-policies\/[a-f0-9]{32}\/events$/;
const nativeID = /^[a-f0-9]{32}$/;

function errorBody(status: number) {
  return { apiVersion, error: {
    code: ({ 400: "invalid-input", 401: "unauthorized", 403: "forbidden", 404: "not-found", 409: "conflict" } as Record<number, string>)[status],
    message: "Synthetic notification policy operation denied.", requestId: "synthetic-notification-policy", retryable: false,
  } };
}

export class NotificationPolicyUIAPI extends JiraUIAPI {
  readonly policyCalls: WorkCall[] = [];
  readonly policyDeclarations: PolicyControl[] = [];
  private readonly policyReplies: Reply[] = [];
  private readonly policyRequests = new Map<Request, WorkCall>();

  readPolicy(path: string, payload: unknown, options: Options = {}): PolicyControl {
    return this.declarePolicy("GET", path, {}, payload, options);
  }
  writePolicy(method: "POST" | "PATCH", path: string, body: Body, payload: unknown, options: Options = {}): PolicyControl {
    return this.declarePolicy(method, path, body, payload, options);
  }
  private declarePolicy(method: string, path: string, body: Body, payload: unknown, options: Options): PolicyControl {
    const { status = 200, workspace = alpha.id, held = false, query = {} } = options;
    if (!this.roles.has(workspace) ||
      !(path === policiesPath || policyDetail.test(path) || policyEvents.test(path))) {
      throw new Error("Declare only bounded workspace notification-policy routes.");
    }
    let release!: () => void;
    const reply: Reply = {
      method, path, body: structuredClone(body), workspace, query: structuredClone(query),
      status, calls: [], closed: false, maximum: 2,
      payload: structuredClone(status >= 400 ? errorBody(status) : payload),
      wait: new Promise<void>((resolve) => { release = resolve; }), release: () => release(), done: [],
    };
    if (!held) release();
    this.policyReplies.push(reply); this.policyDeclarations.push(reply);
    return reply;
  }
  finishPolicy(control: PolicyControl) {
    expect(control.closed).toBe(false);
    expect(control.calls.length).toBeGreaterThan(0); expect(control.calls.length).toBeLessThanOrEqual(control.maximum);
    expect(control.calls.some((call) => call.responseStatus === control.status &&
      call.responseBeforeFailure && call.finished && call.failure === null)).toBe(true);
    control.closed = true;
  }
  override async releaseResponses() {
    for (const reply of this.policyReplies) reply.release();
    await Promise.all([super.releaseResponses(), ...this.policyReplies.flatMap((reply) => reply.done)]);
  }
  override async install(page: Page, origin: string) {
    await super.install(page, origin);
    page.on("response", (response) => {
      const call = this.policyRequests.get(response.request());
      if (call) {
        call.responseStatus = response.status();
        call.responseBeforeFailure = call.failure === null && response.request().failure() === null;
      }
    });
    page.on("requestfinished", (request) => {
      const call = this.policyRequests.get(request); if (call) call.finished = true;
    });
    page.on("requestfailed", (request) => {
      const call = this.policyRequests.get(request); if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.context().route((url) => url.pathname === policiesPath ||
      policyDetail.test(url.pathname) || policyEvents.test(url.pathname), async (route) => {
      const request = route.request(), url = new URL(request.url());
      const call: WorkCall = {
        method: request.method(), path: url.pathname, workspace: undefined, query: Object.fromEntries(url.searchParams),
        body: {}, status: null, response: null, failure: request.failure()?.errorText ?? null,
        responseStatus: null, responseBeforeFailure: false, finished: false,
      };
      this.requests.push(call); this.policyCalls.push(call); this.policyRequests.set(request, call);
      let reply: Reply | undefined, done: (() => void) | undefined;
      try {
        if (url.origin !== origin) { this.externalAttempts++; throw new Error("External policy request blocked."); }
        if (this.requests.length > 60) throw new Error("Combined notification-policy request cap exceeded.");
        const headers = await request.allHeaders(), workspace = headers["x-aspm-workspace-id"];
        call.workspace = workspace;
        if (["authorization", "api-key", "x-api-key", "x-aspm-bootstrap-token", "if-match", "if-unmodified-since",
          "x-aspm-revision", "idempotency-key", "x-idempotency-key"].some((key) => headers[key])) {
          throw new Error("Notification policies use only the current cookie, workspace and Origin authority.");
        }
        const hasCookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${cookie}`);
        if (!workspace || !this.roles.has(workspace) || !this.authenticated || !hasCookie ||
          !this.requests.some((read) => ["/api/v1/session", "/api/v1/login"].includes(read.path) &&
            read.responseStatus === 200 && read.responseBeforeFailure)) {
          throw new Error("Notification-policy traffic requires a current scoped application session.");
        }
        if (url.searchParams.size !== Object.keys(call.query).length) throw new Error("Repeated policy selectors are forbidden.");
        if (call.method === "GET") {
          if (request.postData() !== null) throw new Error("Policy reads have no body.");
          if (policyEvents.test(call.path)) {
            if (call.query.limit !== "100" ||
              Object.keys(call.query).some((key) => !["limit", "cursor"].includes(key)) ||
              "cursor" in call.query && !nativeID.test(call.query.cursor)) {
              throw new Error("Policy event history requires limit=100 and an optional native cursor.");
            }
          } else if (url.search !== "") throw new Error("Policy collection/detail reads have no query.");
        } else {
          if (!(call.method === "POST" && call.path === policiesPath ||
            call.method === "PATCH" && policyDetail.test(call.path))) {
            throw new Error("No undeclared policy mutation is available.");
          }
          const media = headers["content-type"]?.toLowerCase().split(";").map((value) => value.trim());
          if (url.search !== "" || headers.origin !== origin || media?.[0] !== "application/json" ||
            media.some((value) => value.startsWith("charset=") && value !== "charset=utf-8") ||
            (request.postDataBuffer()?.length ?? 0) > 16 << 10) {
            throw new Error("Policy writes require bounded same-origin UTF-8 JSON.");
          }
          const body: unknown = request.postDataJSON();
          if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("Policy write must be an object.");
          call.body = body as Body;
        }
        reply = this.policyReplies.find((value) => !value.closed && value.calls.length < value.maximum &&
          value.method === call.method && value.path === call.path && value.workspace === workspace &&
          isDeepStrictEqual(value.query, call.query));
        if (!reply || !isDeepStrictEqual(reply.body, call.body)) {
          throw new Error(`Undeclared notification-policy intent: ${call.method} ${call.path}.`);
        }
        if (call.method !== "GET" && this.serverRoles.get(workspace) !== "admin" &&
          ![401, 403, 404].includes(reply.status)) throw new Error("Current server role denies policy mutation.");
        reply.calls.push(call); reply.done.push(new Promise<void>((resolve) => { done = resolve; }));
        call.status = reply.status; call.response = structuredClone(reply.payload);
        await reply.wait;
        if (reply.status === 401) this.authenticated = false;
        await route.fulfill({ status: reply.status, json: call.response });
      } catch (cause) { this.violations.push(String(cause)); await route.abort(); }
      finally { done?.(); }
    });
  }
}

export const test = base.extend<{ policies: NotificationPolicyUIAPI }>({
  policies: async ({ page, baseURL }, use, info) => {
    if (!baseURL) throw new Error("Use the real local App browser server.");
    const api = new NotificationPolicyUIAPI();
    await api.install(page, new URL(baseURL).origin);
    try { await use(api); } finally {
      await api.releaseResponses(); api.assertData(); await api.assertPrivate(page);
      expect.soft(api.violations).toEqual([]); expect.soft(api.pageErrors).toEqual([]);
      expect.soft(api.externalAttempts).toBe(0); expect.soft(api.requests.length).toBeLessThanOrEqual(60);
      expect.soft(api.requests.filter((call) => call.method !== "GET" &&
        !["/api/v1/login", "/api/v1/logout"].includes(call.path) && !api.policyCalls.includes(call))).toEqual([]);
      await info.attach("notification-policy-ui-http-ledger", { contentType: "application/json", body: JSON.stringify({
        boundary: "Declared policy metadata at the real App only. No provider, worker or live-vendor qualification.",
        reached: api.reached, requests: api.requests,
        policyRequestIndexes: api.policyCalls.map((call) => api.requests.indexOf(call)),
        declarations: api.policyDeclarations.map(({ method, path, workspace, query, status, closed, calls }) => ({
          method, path, workspace, query, status, closed,
          requestIndexes: calls.map((call) => api.requests.indexOf(call)),
        })),
        violations: api.violations, pageErrors: api.pageErrors, externalAttempts: api.externalAttempts,
      }, null, 2) });
    }
  },
});
export { expect } from "@playwright/test";
