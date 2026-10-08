import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request } from "@playwright/test";
import { NotificationPolicyUIAPI } from "./notification-policies-ui-fixture";
import type { WorkCall } from "./saved-work-views-fixture";
import { cookie } from "./work-search-data";
import {
  alpha, apiVersion, connectionsPath, draftSecret, profile,
  replacementSecret, webhookURL,
} from "./generic-webhook-ui-data";

type Status = 200 | 201 | 202 | 400 | 401 | 403 | 404 | 409 | 503 | "lost-ack";
type Body = Record<string, unknown> | ((body: Record<string, unknown>) => void);
interface Options {
  status?: Status; workspace?: string; held?: boolean; entry?: boolean;
  query?: Record<string, string>; errorMessage?: string;
}
export interface WebhookControl {
  method: string; path: string; workspace: string; query: Record<string, string>;
  calls: WorkCall[]; status: Status; closed: boolean; maximum: number; release: () => void;
}
interface Reply extends WebhookControl {
  body: Body; payload: unknown; wait: Promise<void>; done: Promise<void>[];
}

const nativeID = /^[a-f0-9]{32}$/;
const connectionDetail = /^\/api\/v1\/integrations\/connections\/[a-f0-9]{32}$/;
const deliveryDetail = /^\/api\/v1\/integrations\/deliveries\/[a-f0-9]{32}$/;
const collection = /^\/api\/v1\/findings\/[a-f0-9]{32}\/deliveries$/;
const preview = /^\/api\/v1\/findings\/[a-f0-9]{32}\/delivery-previews$/;
const responseHeaders = {
  "content-type": "application/json; charset=utf-8", "cache-control": "no-store",
  "x-content-type-options": "nosniff", "referrer-policy": "no-referrer",
};

function failure(status: number, message = "Synthetic generic webhook operation denied") {
  return {
    apiVersion,
    error: {
      code: ({
        400: "invalid-input", 401: "unauthorized", 403: "forbidden",
        404: "not-found", 409: "conflict", 503: "unavailable",
      } as Record<number, string>)[status],
      message, requestId: "synthetic-generic-webhook-ui", retryable: false,
    },
  };
}

export class GenericWebhookUIAPI extends NotificationPolicyUIAPI {
  readonly webhookCalls: WorkCall[] = [];
  readonly webhookDeclarations: WebhookControl[] = [];
  readonly webhookSecrets = new Set([draftSecret, replacementSecret]);
  private readonly webhookReplies: Reply[] = [];
  private readonly webhookRequests = new Map<Request, WorkCall>();

  readWebhook(path: string, payload: unknown, options: Options = {}): WebhookControl {
    return this.declareWebhook("GET", path, {}, payload, options);
  }
  listWebhooks(path: string, payload: unknown, options: Options & { cursor?: string } = {}): WebhookControl {
    return this.readWebhook(path, payload, {
      ...options,
      query: { profile, limit: "100", ...options.cursor ? { cursor: options.cursor } : {} },
    });
  }
  writeWebhook(method: "POST" | "PATCH", path: string, body: Body,
    payload: unknown, options: Options = {}): WebhookControl {
    return this.declareWebhook(method, path, body, payload, options);
  }
  private declareWebhook(method: string, path: string, body: Body,
    payload: unknown, options: Options): WebhookControl {
    const { workspace = alpha.id, status = 200, held = false, entry = false, query = {} } = options;
    if (!this.roles.has(workspace) || entry && method !== "GET" ||
      !(path === connectionsPath || connectionDetail.test(path) || deliveryDetail.test(path) ||
        collection.test(path) || preview.test(path))) {
      throw new Error("Declare only bounded generic webhook metadata routes.");
    }
    let release!: () => void;
    const reply: Reply = {
      method, path, workspace, query: structuredClone(query), body,
      status, calls: [], closed: false, maximum: entry ? 2 : 1,
      payload: structuredClone(typeof status === "number" && status >= 400
        ? failure(status, options.errorMessage) : payload),
      wait: new Promise<void>((resolve) => { release = resolve; }),
      release: () => release(), done: [],
    };
    if (!held) release();
    this.webhookReplies.push(reply); this.webhookDeclarations.push(reply);
    return reply;
  }
  finishWebhook(control: WebhookControl) {
    expect(control.closed).toBe(false);
    expect(control.calls.length).toBeGreaterThan(0);
    expect(control.calls.length).toBeLessThanOrEqual(control.maximum);
    if (control.status === "lost-ack") {
      expect(control.calls).toHaveLength(1);
      expect(control.calls[0].failure).not.toBeNull();
    } else {
      expect(control.calls.some((call) => call.responseStatus === control.status &&
        call.responseBeforeFailure && (control.status === 401 || call.finished && call.failure === null))).toBe(true);
    }
    control.closed = true;
  }
  omitWebhook(control: WebhookControl) {
    expect(control.calls).toHaveLength(0);
    control.closed = true;
  }
  finishWebhookAborted(control: WebhookControl) {
    expect(control.calls.length).toBeGreaterThan(0);
    for (const call of control.calls) {
      expect(call.failure, "Scope/session loss must abort the old generic webhook request.").toMatch(/abort/i);
      expect(call.responseStatus).toBeNull();
    }
    control.release(); control.closed = true;
  }
  override async releaseResponses() {
    for (const reply of this.webhookReplies) reply.release();
    await Promise.all([super.releaseResponses(), ...this.webhookReplies.flatMap((reply) => reply.done)]);
  }
  override async assertPrivate(page: Page, cleared = false) {
    await super.assertPrivate(page, cleared);
    if (page.isClosed() || !page.url().startsWith("http://127.0.0.1:")) return;
    const state = await page.evaluate(() => ({
      text: document.body.textContent,
      storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
      fields: [...document.querySelectorAll("input,textarea")].map((field) => ({
        type: field.getAttribute("type"), value: (field as HTMLInputElement).value,
      })),
      attributes: [...document.querySelectorAll("*")].flatMap((element) => [...element.attributes]
        .filter((attribute) => !(element instanceof HTMLInputElement &&
          element.type === "password" && attribute.name === "value"))
        .map((attribute) => attribute.value)),
    }));
    const publicState = JSON.stringify({ ...state, fields: undefined }) +
      page.url() + this.consoleText.join("\n");
    for (const secret of this.webhookSecrets) {
      for (const representation of [secret, encodeURIComponent(secret), Buffer.from(secret).toString("base64")]) {
        expect(publicState, "Webhook HMAC keys never belong in text, attributes, URL, storage or console.")
          .not.toContain(representation);
      }
      for (const field of state.fields.filter((field) => field.value === secret)) {
        expect(field.type).toBe("password");
        expect(cleared, "Closed/successful/invalidated webhook forms must clear secret drafts.").toBe(false);
      }
    }
    const stored = JSON.stringify(state.storage);
    expect(stored).not.toContain(webhookURL);
    if (cleared) {
      expect(state.fields.some((field) => field.value === webhookURL ||
        [...this.webhookSecrets].includes(field.value))).toBe(false);
    }
  }
  override async install(page: Page, origin: string) {
    await super.install(page, origin);
    page.on("response", (response) => {
      const call = this.webhookRequests.get(response.request());
      if (call) {
        call.responseStatus = response.status();
        call.responseBeforeFailure = call.failure === null && response.request().failure() === null;
      }
    });
    page.on("requestfinished", (request) => {
      const call = this.webhookRequests.get(request);
      if (call) call.finished = true;
    });
    page.on("requestfailed", (request) => {
      const call = this.webhookRequests.get(request);
      if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.context().route((url) => url.pathname === connectionsPath ||
      connectionDetail.test(url.pathname) || deliveryDetail.test(url.pathname) ||
      collection.test(url.pathname) || preview.test(url.pathname), async (route) => {
      const request = route.request(), url = new URL(request.url());
      const method = request.method();
      const declared = this.webhookReplies.some((reply) => !reply.closed &&
        reply.method === method && reply.path === url.pathname);
      const genericList = method === "GET" &&
        (url.pathname === connectionsPath || collection.test(url.pathname)) &&
        url.searchParams.get("profile") === profile;
      const declaredDetail = method === "GET" &&
        (connectionDetail.test(url.pathname) || deliveryDetail.test(url.pathname)) && declared;
      const declaredMutation = (method === "POST" || method === "PATCH") && declared;
      if (!genericList && !declaredDetail && !declaredMutation) {
        await route.fallback();
        return;
      }
      const call: WorkCall = {
        method, path: url.pathname, workspace: undefined,
        query: Object.fromEntries(url.searchParams), body: {}, status: null,
        response: null, failure: request.failure()?.errorText ?? null,
        responseStatus: null, responseBeforeFailure: false, finished: false,
      };
      this.requests.push(call); this.webhookCalls.push(call); this.webhookRequests.set(request, call);
      let reply: Reply | undefined, done: (() => void) | undefined;
      try {
        if (url.origin !== origin) { this.externalAttempts++; throw new Error("External webhook request blocked."); }
        if (this.requests.length > 60) throw new Error("Combined generic webhook API request cap exceeded.");
        const headers = await request.allHeaders(), workspace = headers["x-aspm-workspace-id"];
        call.workspace = workspace;
        if (["authorization", "api-key", "x-api-key", "x-aspm-bootstrap-token",
          "if-match", "if-unmodified-since", "x-aspm-revision",
          "idempotency-key", "x-idempotency-key"].some((key) => headers[key]) ||
          Object.entries(headers).some(([key, value]) => key !== "cookie" &&
            [...this.webhookSecrets].some((secret) => value.includes(secret))) ||
          [...this.webhookSecrets].some((secret) => url.href.includes(secret) ||
            url.href.includes(encodeURIComponent(secret)))) {
          throw new Error("Generic webhook browser traffic cannot carry provider credentials or invented authority.");
        }
        const hasCookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${cookie}`);
        if (!workspace || !this.roles.has(workspace) || !hasCookie ||
          !this.requests.some((read) => ["/api/v1/session", "/api/v1/login"].includes(read.path) &&
            read.responseStatus === 200 && read.responseBeforeFailure)) {
          throw new Error("Generic webhook metadata requires a current scoped application session.");
        }
        if (url.searchParams.size !== Object.keys(call.query).length) {
          throw new Error("Repeated generic webhook selectors are forbidden.");
        }
        if (call.method === "GET") {
          if (request.postData() !== null) throw new Error("Generic webhook GET has no body.");
          if (call.path === connectionsPath || collection.test(call.path)) {
            if (call.query.profile !== profile || call.query.limit !== "100" ||
              Object.keys(call.query).some((key) => !["profile", "limit", "cursor"].includes(key)) ||
              "cursor" in call.query && !nativeID.test(call.query.cursor)) {
              throw new Error("Generic webhook list requires exact profile, limit and native cursor.");
            }
          } else if (url.search !== "") throw new Error("Generic webhook detail has no query.");
        } else {
          const configuring = call.path === connectionsPath || connectionDetail.test(call.path);
          if (!(call.method === "POST" &&
            (call.path === connectionsPath || collection.test(call.path) || preview.test(call.path)) ||
            call.method === "PATCH" && connectionDetail.test(call.path))) {
            throw new Error("No test-send, retry, provider or extra webhook mutation is declared.");
          }
          const media = headers["content-type"]?.toLowerCase().split(";").map((value) => value.trim());
          if (url.search !== "" || headers.origin !== origin || media?.[0] !== "application/json" ||
            media.some((value) => value.startsWith("charset=") && value !== "charset=utf-8") ||
            headers["content-encoding"] && headers["content-encoding"] !== "identity" ||
            (request.postDataBuffer()?.length ?? 0) > (configuring ? 32 : 16) * 1024) {
            throw new Error("Generic webhook writes require bounded same-origin UTF-8 JSON.");
          }
          const body: unknown = request.postDataJSON();
          if (!body || typeof body !== "object" || Array.isArray(body)) {
            throw new Error("Generic webhook write must be a JSON object.");
          }
          call.body = body as Record<string, unknown>;
        }
        reply = this.webhookReplies.find((value) => !value.closed && value.calls.length < value.maximum &&
          value.method === call.method && value.path === call.path &&
          value.workspace === workspace && isDeepStrictEqual(value.query, call.query));
        if (!reply) throw new Error(`Undeclared generic webhook intent: ${call.method} ${call.path}.`);
        if (!this.authenticated && reply.status !== 401) {
          throw new Error("No successful generic webhook fallthrough after session loss.");
        }
        if (typeof reply.body === "function") reply.body(call.body);
        else if (!isDeepStrictEqual(reply.body, call.body)) {
          throw new Error("Generic webhook request differs from its exact explicit intent.");
        }
        const configuring = call.path === connectionsPath || connectionDetail.test(call.path);
        if (call.method !== "GET" && (configuring ? this.serverRoles.get(workspace) !== "admin" :
          !["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) &&
          ![401, 403, 404].includes(reply.status as number)) {
          throw new Error("Current server role denies generic webhook mutation.");
        }
        reply.calls.push(call);
        reply.done.push(new Promise<void>((resolve) => { done = resolve; }));
        call.status = reply.status === "lost-ack" ? 202 : reply.status;
        call.response = structuredClone(reply.payload);
        await reply.wait;
        if (reply.status === 401) this.authenticated = false;
        if (reply.status === "lost-ack") await route.abort("failed");
        else await route.fulfill({
          status: reply.status, headers: responseHeaders,
          contentType: responseHeaders["content-type"], json: call.response,
        });
      } catch (cause) {
        this.violations.push(String(cause));
        await route.abort();
      } finally { done?.(); }
    });
  }
}

export const test = base.extend<{ webhooks: GenericWebhookUIAPI }>({
  webhooks: async ({ page, baseURL }, use, info) => {
    if (!baseURL) throw new Error("Use the real local App browser server.");
    const api = new GenericWebhookUIAPI();
    await api.install(page, new URL(baseURL).origin);
    try { await use(api); } finally {
      await api.releaseResponses(); api.assertData(); await api.assertPrivate(page);
      expect.soft(api.violations).toEqual([]);
      expect.soft(api.pageErrors).toEqual([]);
      expect.soft(api.externalAttempts).toBe(0);
      expect.soft(api.requests.length).toBeLessThanOrEqual(60);
      expect.soft(api.requests.filter((call) => call.method !== "GET" &&
        !["/api/v1/login", "/api/v1/logout"].includes(call.path) &&
        !api.webhookCalls.includes(call) && !api.policyCalls.includes(call))).toEqual([]);
      await info.attach("generic-webhook-ui-http-ledger", {
        contentType: "application/json",
        body: JSON.stringify({
          boundary: "Real App and declared local metadata only. No receiver, worker or live-vendor qualification.",
          reached: api.reached, requests: api.requests,
          webhookRequestIndexes: api.webhookCalls.map((call) => api.requests.indexOf(call)),
          declarations: api.webhookDeclarations.map(
            ({ method, path, workspace, query, status, closed, maximum, calls }) => ({
              method, path, workspace, query, status, closed, maximum,
              requestIndexes: calls.map((call) => api.requests.indexOf(call)),
            })),
          violations: api.violations, pageErrors: api.pageErrors,
          externalAttempts: api.externalAttempts,
        }, (key, value: unknown) => ["secret", "password", "idempotencyKey", "webhookUrl"].includes(key)
          ? "[redacted synthetic private input]"
          : typeof value === "string"
          ? [...api.webhookSecrets].reduce((text, secret) =>
              text.replaceAll(secret, "[redacted synthetic private input]"), value)
            : value, 2),
      });
    }
  },
});
export { expect } from "@playwright/test";
