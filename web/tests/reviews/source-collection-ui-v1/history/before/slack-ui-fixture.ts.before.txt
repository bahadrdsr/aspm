import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import { bootstrapToken, password, sessionCookie, wrongPassword } from "./application-fixture";
import { catalogResponse } from "./fixtures";
import { emptyReport, overviewDays, overviewPath, snapshotParameters, snapshotsPath, withFreshness } from "./reports-data";
import {
  alphaConnection, apiVersion, backendID, betaConnection, betaSlackFinding, changedAt, connectionPath, connectionsPath,
  deliveryResult, disabledConnection, draftToken, exactKeys, hexID, paginated, queuedDelivery,
  replacementToken, sameIntent, slackAlpha, slackBeta, slackCookie, slackFinding, slackUser,
  validChannel, validateConnection, validateDelivery, validText, validToken, workItem, workPath,
} from "./slack-ui-data";
import type { DeliveryState, SlackConnection, SlackDelivery, SlackPage, SlackRole } from "./slack-ui-data";
import type { ActionFinding } from "./finding-actions-data";

type Denial = 400 | 401 | 403 | 404 | 409 | 503;
type Status = 200 | 201 | 202 | Denial;
export interface SlackCall {
  method: string; path: string; workspace: string | undefined; query: Record<string, string>;
  body: Record<string, unknown>; status: number | null; response: Record<string, unknown> | null; failure: string | null;
}
export interface SlackControl {
  requested: Promise<void>; delivered: Promise<void>; release: () => void; call: SlackCall | null;
  calls: SlackCall[];
}
interface Gate extends SlackControl { wait: Promise<void>; arrive: () => void; complete: () => void }
interface Scheduled { status: Status | "drop-ack"; gate: Gate }
const canaries = [draftToken, replacementToken, password, wrongPassword, bootstrapToken, sessionCookie, slackCookie];
const terminal = ["confirmed", "accepted", "blocked", "failed", "rate-limited", "uncertain"];

export class SlackUIAPI {
  authenticated = true;
  encryptionAvailable = true;
  readonly roles = new Map<string, SlackRole>([[slackAlpha.id, "admin"], [slackBeta.id, "analyst"]]);
  readonly serverRoles = new Map(this.roles);
  readonly requests: SlackCall[] = [];
  readonly violations: string[] = [];
  readonly pageErrors: string[] = [];
  readonly consoleText: string[] = [];
  externalHTTPAttempts = 0;
  readonly connections = new Map<string, SlackConnection>();
  readonly deliveries = new Map<string, SlackDelivery>();
  readonly findings = new Map<string, ActionFinding>();
  readonly pages: Array<{ call: SlackCall; response: SlackPage<SlackConnection | SlackDelivery> }> = [];
  private sessionCompleted = false;
  private connectionSequence = 0;
  private deliverySequence = 1000;
  private gates = new Set<Gate>();
  private replies = new Map<string, Scheduled[]>();
  private readReplies = new Map<string, Scheduled>();
  private byRequest = new Map<Request, SlackCall>();
  private intents = new Map<string, { id: string; binding: string }>();

  constructor() {
    for (const value of [alphaConnection, disabledConnection, betaConnection]) this.seedConnection(value);
    this.seedFinding(slackFinding);
    this.seedFinding(betaSlackFinding);
  }
  calls(method: string, path: string) { return this.requests.filter((call) => call.method === method && call.path === path); }
  seedConnection(value: SlackConnection) {
    validateConnection(value);
    if (!this.roles.has(value.workspaceId)) throw new Error("Connection must use a known synthetic workspace.");
    this.connections.set(value.id, structuredClone(value));
  }
  seedConnections(workspace: string, values: readonly SlackConnection[]) {
    if (!this.roles.has(workspace) || values.some((value) => value.workspaceId !== workspace) ||
      new Set(values.map((value) => value.id)).size !== values.length) throw new Error("Invalid synthetic connection collection.");
    for (const [id, value] of this.connections) if (value.workspaceId === workspace) this.connections.delete(id);
    for (const value of values) this.seedConnection(value);
  }
  seedFinding(value: ActionFinding) {
    if (!backendID(value.id) || !this.roles.has(value.workspaceId)) throw new Error("Invalid synthetic finding.");
    this.findings.set(value.id, structuredClone(value));
  }
  seedDelivery(value: SlackDelivery) {
    validateDelivery(value);
    const connection = this.connections.get(value.connectionId), finding = this.findings.get(value.findingId);
    if (!connection || connection.workspaceId !== value.workspaceId || !finding || finding.workspaceId !== value.workspaceId) {
      throw new Error("Seed only workspace-owned delivery responses.");
    }
    const prior = this.deliveries.get(value.id);
    if (prior && (!sameIntent(prior, value) || terminal.includes(prior.state) && JSON.stringify(prior) !== JSON.stringify(value))) {
      throw new Error("Synthetic response publication must not rewrite an immutable intent or terminal outcome.");
    }
    this.deliveries.set(value.id, structuredClone(value));
  }
  publishResult(id: string, state: Exclude<DeliveryState, "accepted">) {
    const prior = this.deliveries.get(id);
    if (!prior || terminal.includes(prior.state)) throw new Error("Only a known nonterminal response may advance.");
    const next = deliveryResult(prior, state);
    this.seedDelivery(next);
    return structuredClone(next);
  }
  private schedule(method: string, path: string, workspace: string, status: Status | "drop-ack", held: boolean) {
    if (!this.roles.has(workspace)) throw new Error("Schedule only known synthetic workspace responses.");
    let release!: () => void, arrive!: () => void, complete!: () => void;
    const gate: Gate = {
      wait: new Promise<void>((resolve) => { release = resolve; }),
      requested: new Promise<void>((resolve) => { arrive = resolve; }),
      delivered: new Promise<void>((resolve) => { complete = resolve; }),
      release: () => release(), arrive: () => arrive(), complete: () => complete(), call: null, calls: [],
    };
    const key = `${method} ${path} ${workspace}`;
    // A read scenario survives an aborted subscription restart until the test sets the next HTTP state.
    if (method === "GET") this.readReplies.set(key, { status, gate });
    else this.replies.set(key, [...(this.replies.get(key) ?? []), { status, gate }]);
    this.gates.add(gate);
    if (!held) release();
    return gate;
  }
  queueRead(path: string, workspace = slackAlpha.id, status: 200 | Denial = 200, held = false) {
    return this.schedule("GET", path, workspace, status, held);
  }
  queueCreate(status: 201 | Denial = 201, held = false, workspace = slackAlpha.id) {
    return this.schedule("POST", connectionsPath, workspace, status, held);
  }
  queuePatch(id: string, status: 200 | Denial = 200, held = false) {
    const connection = this.connections.get(id);
    if (!connection) throw new Error("Patch controls require an existing synthetic connection.");
    return this.schedule("PATCH", connectionPath(id), connection.workspaceId, status, held);
  }
  queueEnqueue(status: 202 | 200 | Denial | "drop-ack" = 202, held = false, finding = slackFinding) {
    return this.schedule("POST", `/api/v1/findings/${finding.id}/deliveries`, finding.workspaceId, status, held);
  }
  async releaseResponses() {
    const arrived = [...this.gates].filter((gate) => gate.call !== null);
    for (const gate of this.gates) gate.release();
    await Promise.all(arrived.map((gate) => gate.delivered));
  }
  private error(status: number) {
    const messages: Record<number, [string, string]> = {
      400: ["invalid-input", "The request is invalid"],
      401: ["unauthorized", "Authentication is required"],
      403: ["forbidden", "This operation is not permitted"],
      404: ["not-found", "The requested resource was not found"],
      409: ["conflict", "The request conflicts with existing state"],
      413: ["too-large", "The request exceeds the configured size limit"],
      415: ["unsupported-format", "The content or report format is not supported"],
      429: ["rate-limited", "Synthetic request budget exceeded"],
      503: ["unavailable", "The operation could not be completed"],
    };
    const [code, message] = messages[status] ?? messages[503];
    return { apiVersion, error: { code, message, requestId: "synthetic-slack-ui-request", retryable: false } };
  }
  private async deliver(route: Route, call: SlackCall, status: number, response: Record<string, unknown>, scheduled?: Scheduled) {
    const snapshot = structuredClone(response);
    call.status = status;
    call.response = snapshot;
    const gate = scheduled?.gate;
    try {
      if (gate) { gate.call = call; gate.calls.push(call); gate.arrive(); await gate.wait; }
      if (status === 401) this.authenticated = false;
      if (scheduled?.status === "drop-ack" && status < 400) await route.abort("failed");
      else await route.fulfill({ status, json: snapshot });
    } finally {
      gate?.complete();
      if (gate) this.gates.delete(gate);
    }
  }
  private async read(route: Route, call: SlackCall, value: Record<string, unknown>, scheduled?: Scheduled) {
    const status = scheduled?.status ?? 200;
    if (status === "drop-ack") throw new Error("Only an enqueue acknowledgement may be dropped.");
    await this.deliver(route, call, status, status === 200 ? value : this.error(status), scheduled);
  }
  private work(workspace: string, url: URL) {
    const q = url.searchParams.get("q") ?? "";
    if (!validText(q || "empty query", 512) || url.searchParams.getAll("q").length > 1) throw new Error("Invalid Work query.");
    const listURL = new URL(url);
    listURL.searchParams.delete("q");
    const values = [...this.findings.values()].filter((finding) => finding.workspaceId === workspace &&
      `${finding.title}\n${finding.assetName}\n${finding.ownerName ?? ""}`.toLowerCase().includes(q.toLowerCase())).map(workItem);
    return paginated(values, listURL);
  }
  private connectionWrite(call: SlackCall): SlackConnection | null {
    const body = call.body;
    if (call.method === "POST") {
      if (!exactKeys(body, ["profile", "name", "channel", "token", "enabled"]) ||
        body.profile !== "slack-workspace-bot" || !validText(body.name, 256) ||
        !validChannel(body.channel) || !validToken(body.token) || typeof body.enabled !== "boolean") return null;
      return {
        id: hexID("c3", ++this.connectionSequence), workspaceId: call.workspace!, profile: body.profile,
        name: body.name, channel: body.channel, enabled: body.enabled, credentialConfigured: true,
        revision: 1, createdAt: changedAt, updatedAt: changedAt,
      };
    }
    const prior = this.connections.get(call.path.split("/").at(-1)!);
    if (!prior || Object.keys(body).some((key) => !["name", "channel", "token", "enabled"].includes(key)) ||
      "name" in body && !validText(body.name, 256) || "channel" in body && !validChannel(body.channel) ||
      "token" in body && !validToken(body.token) || "enabled" in body && typeof body.enabled !== "boolean") return null;
    const next = structuredClone(prior);
    if (typeof body.name === "string") next.name = body.name;
    if (typeof body.channel === "string") next.channel = body.channel;
    if (typeof body.enabled === "boolean") next.enabled = body.enabled;
    if (next.name !== prior.name || next.channel !== prior.channel || next.enabled !== prior.enabled || "token" in body) {
      next.revision += 1;
      next.updatedAt = changedAt;
    }
    return next;
  }
  async assertPrivate(page: Page, closed = false) {
    if (page.isClosed()) return;
    const snapshot = await page.evaluate(() => ({
      text: document.body.textContent, visible: document.body.innerText, cookie: document.cookie,
      storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
      fields: [...document.querySelectorAll("input, textarea")].map((element) => ({
        type: element.getAttribute("type"), value: (element as HTMLInputElement).value,
      })),
    }));
    const publicText = JSON.stringify({ ...snapshot, fields: undefined }) + page.url() + this.consoleText.join("\n");
    for (const secret of canaries) {
      for (const representation of [secret, encodeURIComponent(secret), Buffer.from(secret).toString("base64")]) {
        expect(publicText, "Synthetic credentials must not appear in text, URL, readable cookies, browser storage or console.").not.toContain(representation);
      }
    }
    for (const entry of snapshot.fields.filter((field) => [draftToken, replacementToken].includes(field.value))) {
      expect(entry.type, "A drafted bot token must remain masked.").toBe("password");
      expect(closed, "Closing/success/session invalidation must remove token drafts, including hidden fields.").toBe(false);
    }
    for (const [key, value] of snapshot.storage) {
      expect(key, "No connection, delivery, finding or credential data belongs in browser storage.").toBe("aspm.theme");
      expect(["light", "dark"]).toContain(value);
    }
  }
  async install(page: Page, origin: string) {
    const baseURL = new URL(origin);
    if (baseURL.protocol !== "http:" || baseURL.hostname !== "127.0.0.1") throw new Error("Only the existing loopback UI HTTP fixture is authorized.");
    await page.context().addCookies([{ name: "aspm_session", value: slackCookie, url: origin, httpOnly: true, sameSite: "Lax" }]);
    page.on("pageerror", (error) => this.pageErrors.push(error.message));
    page.on("console", (message) => this.consoleText.push(message.text()));
    page.on("requestfailed", (request) => {
      const call = this.byRequest.get(request);
      if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) {
        this.externalHTTPAttempts += 1;
        this.violations.push("No provider, OAuth, vendor, remote evidence or other external HTTP traffic is authorized.");
        await route.abort();
        return;
      }
      if (canaries.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret)))) {
        this.violations.push("A synthetic credential appeared in a request URL.");
      }
      if (!url.pathname.startsWith("/api/")) { await route.continue(); return; }
      const headers = await request.allHeaders(), method = request.method(), path = url.pathname;
      const call: SlackCall = {
        method, path, workspace: headers["x-aspm-workspace-id"], query: Object.fromEntries(url.searchParams),
        body: {}, status: null, response: null, failure: null,
      };
      this.requests.push(call);
      this.byRequest.set(request, call);
      if (this.requests.length > 60) {
        this.violations.push("The unchanged combined 60-API-request per-case budget was exceeded.");
        await this.deliver(route, call, 429, this.error(429));
        return;
      }
      if (headers.authorization || headers["x-aspm-bootstrap-token"] ||
        Object.entries(headers).some(([key, value]) => key !== "cookie" && [draftToken, replacementToken].some((token) => value.includes(token)))) {
        this.violations.push("Browser authority is the HttpOnly application session, never provider/bootstrap/bearer headers.");
      }
      if (headers["if-match"] || headers["if-unmodified-since"] || headers["x-aspm-revision"]) {
        this.violations.push("Revision is returned metadata, not an invented conditional-write header.");
      }
      const collection = /^\/api\/v1\/findings\/([a-f0-9]{32})\/deliveries$/.exec(path);
      const connectionID = /^\/api\/v1\/integrations\/connections\/([a-f0-9]{32})$/.exec(path)?.[1];
      const deliveryID = /^\/api\/v1\/integrations\/deliveries\/([a-f0-9]{32})$/.exec(path)?.[1];
      const findingID = /^\/api\/v1\/findings\/([a-f0-9]{32})$/.exec(path)?.[1];
      const write = method === "POST" && [connectionsPath, "/api/v1/login", "/api/v1/logout"].includes(path) ||
        method === "POST" && collection !== null || method === "PATCH" && connectionID !== undefined;
      if (method !== "GET" && !write) {
        this.violations.push(`Undeclared write: ${method} ${path}. No finding mutation, retry, dispatch or provider operation exists in this slice.`);
        await route.abort();
        return;
      }
      if (write) {
        if (headers.origin !== origin || url.search !== "") this.violations.push("Writes require matching Origin and JSON fields, not query authority.");
        if (path !== "/api/v1/logout") {
          let parsed: unknown;
          try { parsed = request.postDataJSON(); } catch { parsed = null; }
          const type = headers["content-type"]?.split(";").map((part) => part.trim().toLowerCase());
          if (type?.[0] !== "application/json" || type.some((part) => part.startsWith("charset=") && part !== "charset=utf-8") ||
            headers["content-encoding"] && headers["content-encoding"] !== "identity" ||
            parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
            this.violations.push("Connection/delivery writes require an unencoded UTF-8 JSON object.");
            await this.deliver(route, call, 400, this.error(400));
            return;
          }
          call.body = parsed as Record<string, unknown>;
          const allowedFields = collection ? ["connectionId", "idempotencyKey"] :
            connectionID ? ["name", "channel", "token", "enabled"] :
              path === connectionsPath ? ["profile", "name", "channel", "token", "enabled"] : ["email", "password"];
          if (Object.keys(call.body).some((key) => !allowedFields.includes(key))) {
            this.violations.push("Unknown write fields remain prohibited even when the current server role or configuration denies the request.");
          }
          if (Buffer.byteLength(request.postData() ?? "", "utf8") > (collection ? 16 << 10 : 32 << 10)) {
            this.violations.push("The native 16 KiB enqueue / 32 KiB connection JSON limit was exceeded.");
            await this.deliver(route, call, 413, this.error(413));
            return;
          }
        }
      }
      const cookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${slackCookie}`);
      const session = () => ({
        apiVersion, user: slackUser, expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        workspaces: [slackAlpha, slackBeta].map((value) => ({ ...value, role: this.roles.get(value.id)! })),
      });
      if (method === "GET" && path === "/api/v1/session" && url.search === "") {
        if (this.authenticated && cookie) { await this.deliver(route, call, 200, session()); this.sessionCompleted = true; }
        else await this.deliver(route, call, 401, this.error(401));
        return;
      }
      if (method === "POST" && path === "/api/v1/login") {
        if (!exactKeys(call.body, ["email", "password"]) || call.body.email !== slackUser.email || call.body.password !== password) {
          await this.deliver(route, call, 401, this.error(401));
        } else {
          this.authenticated = true;
          call.status = 200; call.response = session();
          await route.fulfill({ json: call.response, headers: { "set-cookie": `aspm_session=${slackCookie}; Path=/; HttpOnly; SameSite=Lax` } });
          this.sessionCompleted = true;
        }
        return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected data started before the authenticated session completed.");
      if (!this.authenticated || !cookie) { await this.deliver(route, call, 401, this.error(401)); return; }
      if (method === "POST" && path === "/api/v1/logout") {
        this.authenticated = false; this.sessionCompleted = false; call.status = 204;
        await route.fulfill({ status: 204, headers: { "set-cookie": "aspm_session=; Max-Age=0; Path=/; HttpOnly; SameSite=Lax" } });
        return;
      }
      const workspace = call.workspace;
      if (!workspace || !this.roles.has(workspace)) {
        this.violations.push("Protected data omitted or invented the selected session workspace.");
        await this.deliver(route, call, 403, this.error(403));
        return;
      }
      const key = `${method} ${path} ${workspace}`;
      const scheduled = method === "GET" ? this.readReplies.get(key) : this.replies.get(key)?.shift();
      if (!this.serverRoles.has(workspace)) { await this.deliver(route, call, 403, this.error(403), scheduled); return; }
      try {
        if (method === "GET" && path === connectionsPath) {
          const response = paginated([...this.connections.values()].filter((value) => value.workspaceId === workspace), url);
          if (!scheduled || scheduled.status === 200) this.pages.push({ call, response: structuredClone(response) });
          await this.read(route, call, { ...response }, scheduled);
        } else if (method === "GET" && connectionID) {
          if (url.search !== "") throw new Error("Connection detail has no query fields.");
          const connection = this.connections.get(connectionID);
          if (!connection || connection.workspaceId !== workspace) await this.deliver(route, call, 404, this.error(404), scheduled);
          else await this.read(route, call, { apiVersion, dataOrigin: "synthetic", connection }, scheduled);
        } else if (method === "GET" && collection) {
          const finding = this.findings.get(collection[1]);
          if (!finding || finding.workspaceId !== workspace) { await this.deliver(route, call, 404, this.error(404), scheduled); return; }
          const response = paginated([...this.deliveries.values()].filter((value) =>
            value.workspaceId === workspace && value.findingId === finding.id), url);
          if (!scheduled || scheduled.status === 200) this.pages.push({ call, response: structuredClone(response) });
          await this.read(route, call, { ...response }, scheduled);
        } else if (method === "GET" && deliveryID) {
          if (url.search !== "") throw new Error("Delivery detail has no query fields.");
          const delivery = this.deliveries.get(deliveryID);
          if (!delivery || delivery.workspaceId !== workspace) await this.deliver(route, call, 404, this.error(404), scheduled);
          else await this.read(route, call, { apiVersion, dataOrigin: "synthetic", delivery }, scheduled);
        } else if (write && (path === connectionsPath || connectionID)) {
          let status = scheduled?.status ?? (method === "POST" ? 201 : 200);
          if (status === "drop-ack") throw new Error("Connection writes cannot use the enqueue drop control.");
          if (this.serverRoles.get(workspace) !== "admin") status = 403;
          else if (connectionID && this.connections.get(connectionID)?.workspaceId !== workspace) status = 404;
          else if (!this.encryptionAvailable && (method === "POST" || "token" in call.body)) status = 503;
          if (status >= 400) { await this.deliver(route, call, status, this.error(status), scheduled); return; }
          const connection = this.connectionWrite(call);
          if (!connection) {
            this.violations.push("Connection JSON must contain only bounded intentional profile/name/channel/token/enabled fields; PATCH is sparse.");
            await this.deliver(route, call, 400, this.error(400), scheduled);
          } else {
            this.seedConnection(connection);
            await this.deliver(route, call, status, { apiVersion, dataOrigin: "synthetic", connection }, scheduled);
          }
        } else if (method === "POST" && collection) {
          let status: number = scheduled?.status === "drop-ack" ? 202 : scheduled?.status ?? 202;
          const body = call.body, finding = this.findings.get(collection[1]);
          const connection = typeof body.connectionId === "string" ? this.connections.get(body.connectionId) : undefined;
          if (!exactKeys(body, ["connectionId", "idempotencyKey"]) || !backendID(body.connectionId) || !validText(body.idempotencyKey, 256)) {
            this.violations.push("Enqueue accepts exactly connectionId and a generated bounded idempotencyKey, not payload, actor or authority.");
            status = 400;
          } else if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) status = 403;
          else if (!finding || finding.workspaceId !== workspace || !connection || connection.workspaceId !== workspace) status = 404;
          else if (!connection.enabled) status = 409;
          if (status >= 400 || !finding || !connection) { await this.deliver(route, call, status, this.error(status), scheduled); return; }
          const proposed = queuedDelivery(connection, finding, ++this.deliverySequence);
          const binding = JSON.stringify({
            workspace, finding: finding.id, connection: connection.id, revision: connection.revision,
            channel: connection.channel, profile: connection.profile, actor: slackUser.id, payload: proposed.payload,
          });
          const key = `${workspace}:${String(body.idempotencyKey)}`, prior = this.intents.get(key);
          if (prior && prior.binding !== binding) { await this.deliver(route, call, 409, this.error(409), scheduled); return; }
          const delivery = prior ? this.deliveries.get(prior.id)! : proposed;
          if (!prior) {
            this.seedDelivery(delivery);
            this.intents.set(key, { id: delivery.id, binding });
          }
          await this.deliver(route, call, prior ? 200 : 202, { apiVersion, dataOrigin: "synthetic", delivery }, scheduled);
        } else if (method === "GET" && path === workPath) {
          await this.read(route, call, { ...this.work(workspace, url) }, scheduled);
        } else if (method === "GET" && findingID) {
          const finding = this.findings.get(findingID);
          if (url.search !== "") throw new Error("This bounded finding response has no additional pages.");
          if (!finding || finding.workspaceId !== workspace) await this.deliver(route, call, 404, this.error(404), scheduled);
          else await this.read(route, call, { apiVersion, dataOrigin: "synthetic", finding }, scheduled);
        } else if (method === "GET" && path === "/api/v1/integrations/catalog" && url.search === "") {
          await this.read(route, call, { ...catalogResponse }, scheduled);
        } else if (method === "GET" && path === "/api/v1/assets" && url.search === "") {
          await this.read(route, call, { apiVersion, dataOrigin: "synthetic", items: [], total: 0, nextCursor: null }, scheduled);
        } else if (method === "GET" && path === overviewPath) {
          await this.read(route, call, { apiVersion, dataOrigin: "synthetic", report: withFreshness(emptyReport(workspace), overviewDays(url)) }, scheduled);
        } else if (method === "GET" && path === snapshotsPath) {
          snapshotParameters(url);
          await this.read(route, call, { apiVersion, dataOrigin: "synthetic", items: [], total: 0, nextCursor: null }, scheduled);
        } else {
          this.violations.push(`Undeclared HTTP operation: ${method} ${path}. No member directory, verification, dispatch or retry route is permitted.`);
          await this.deliver(route, call, 404, this.error(404), scheduled);
        }
      } catch (error) {
        this.violations.push(String(error));
        scheduled?.gate.complete();
        throw error;
      }
    });
  }
}

export const test = base.extend<{ slack: SlackUIAPI }>({
  slack: async ({ page, baseURL }, use, testInfo) => {
    if (!baseURL) throw new Error("Slack UI tests require the unchanged loopback browser configuration.");
    const slack = new SlackUIAPI();
    await slack.install(page, new URL(baseURL).origin);
    try { await use(slack); } finally {
      await slack.releaseResponses();
      expect.soft(slack.violations, "Only declared HTTP responses at the guarded boundary, with the original combined 60-call budget.").toEqual([]);
      expect.soft(slack.pageErrors, "The real UI must not raise unhandled browser errors.").toEqual([]);
      await slack.assertPrivate(page);
      await testInfo.attach("slack-ui-http-ledger", {
        body: JSON.stringify({
          requests: slack.requests.map((call) => ({
            ...call, body: call.path === "/api/v1/login" ? { credentials: "redacted synthetic sign-in" } :
              "token" in call.body ? { ...call.body, token: "redacted synthetic bot credential" } : call.body,
          })),
          violations: slack.violations, pageErrors: slack.pageErrors,
          externalHTTPAttempts: slack.externalHTTPAttempts,
          boundary: "Synthetic HTTP responses only; no native network access, backend or worker is authorized.",
        }, null, 2), contentType: "application/json",
      });
    }
  },
});
export { expect } from "@playwright/test";
