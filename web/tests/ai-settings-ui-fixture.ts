import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { isDeepStrictEqual } from "node:util";
import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import { catalogResponse } from "./fixtures";
import {
  aiAlpha, aiBeta, aiCookie, aiGamma, aiPassword, aiUser, apiVersion, backendID, betaProfile, changedAt,
  defaultPolicy, disabledProfile, draftKey, exactKeys, gammaProfile, grantFields, grantsPath, hostedProfile,
  localProfile, modes, nativeID, pageOf, pageParameters, policyPath, profileBodyValid, profileFields, profilesPath,
  replacementKey, revokedAt, timestamp, unreviewedProfile,
} from "./ai-settings-ui-data";
import type { AIGrant, AIPage, AIPolicy, AIProfile, Family, Mode, Role } from "./ai-settings-ui-data";

type Status = 200 | 201 | 400 | 401 | 403 | 404 | 409 | 503;
export interface AICall {
  method: string; path: string; workspace: string | undefined; query: Record<string, string>; body: Record<string, unknown>;
  status: number | null; response: Record<string, unknown> | null; failure: string | null;
}
export interface AIControl {
  call: AICall | null; calls: AICall[]; delivered: Promise<void>; release: () => void;
}
interface Gate extends AIControl { wait: Promise<void>; complete: () => void }
interface Reply { status: Status; gate: Gate; errorMessage?: string }
const canaries = [draftKey, replacementKey, aiPassword, aiCookie];
const workspaces = [aiAlpha, aiBeta, aiGamma];
const authorInputs = [
  "ai-settings-ui-data.ts", "ai-settings-ui-fixture.ts", "ai-settings-ui.spec.ts",
  "reviews/ai-settings-ui-v1/CONTRACT.txt", "reviews/ai-settings-ui-v1/playwright.config.ts",
];

function sourceWitnesses() {
  return authorInputs.map((file) => {
    const bytes = readFileSync(new URL(file, import.meta.url));
    return { path: `web\\tests\\${file.replaceAll("/", "\\")}`, bytes: bytes.length, sha256: createHash("sha256").update(bytes).digest("hex") };
  });
}

export class AISettingsHTTP {
  authenticated = true;
  settingsOpen = false;
  encryptionAvailable = true;
  readonly roles = new Map<string, Role>([[aiAlpha.id, "admin"], [aiBeta.id, "analyst"], [aiGamma.id, "viewer"]]);
  readonly serverRoles = new Map(this.roles);
  readonly profiles = new Map<string, AIProfile>();
  readonly policies = new Map<string, AIPolicy>();
  readonly grants = new Map<string, AIGrant>();
  readonly requests: AICall[] = [];
  readonly pages: Array<{ call: AICall; response: AIPage<AIProfile | AIGrant> }> = [];
  readonly violations: string[] = [];
  readonly pageErrors: string[] = [];
  readonly consoleText: string[] = [];
  readonly reached: string[] = [];
  externalAttempts = 0;
  private sessionCompleted = false;
  private sequence = 0;
  private readReplies = new Map<string, Reply>();
  private writeReplies = new Map<string, Reply[]>();
  private gates = new Set<Gate>();
  private byRequest = new Map<Request, AICall>();

  constructor() {
    for (const profile of [hostedProfile, localProfile, disabledProfile, unreviewedProfile, betaProfile, gammaProfile]) this.seedProfile(profile);
    for (const workspace of workspaces) this.policies.set(workspace.id, defaultPolicy(workspace.id));
  }
  mark(phase: string) { this.reached.push(phase); }
  calls(method: string, path: string) { return this.requests.filter((call) => call.method === method && call.path === path); }
  aiCalls() { return this.requests.filter((call) => call.path.startsWith("/api/v1/ai/")); }
  seedProfile(profile: AIProfile) {
    if (!backendID(profile.id) || !this.roles.has(profile.workspaceId) || !profile.revision ||
      !profileBodyValid({}, profile)) throw new Error("Invalid nonsecret synthetic profile metadata.");
    this.profiles.set(profile.id, structuredClone(profile));
  }
  seedProfiles(workspace: string, profiles: AIProfile[]) {
    if (profiles.length > 101 || profiles.some((profile) => profile.workspaceId !== workspace)) throw new Error("Only bounded workspace-owned profile pages.");
    for (const [id, profile] of this.profiles) if (profile.workspaceId === workspace) this.profiles.delete(id);
    for (const profile of profiles) this.seedProfile(profile);
  }
  seedGrant(grant: AIGrant) {
    if (!backendID(grant.id) || !this.roles.has(grant.workspaceId) || !grant.profileRevision || !grant.policyRevision ||
      !timestamp(grant.expiresAt) || this.profiles.get(grant.profileId)?.workspaceId !== grant.workspaceId) {
      throw new Error("Seed only bounded, workspace-owned grant metadata, never an arbitrary approval reference.");
    }
    this.grants.set(grant.id, structuredClone(grant));
  }
  private control(held: boolean): Gate {
    let release!: () => void, complete!: () => void;
    const gate: Gate = {
      call: null, calls: [], wait: new Promise<void>((resolve) => { release = resolve; }),
      delivered: new Promise<void>((resolve) => { complete = resolve; }), release: () => release(), complete: () => complete(),
    };
    this.gates.add(gate);
    if (!held) release();
    return gate;
  }
  queueRead(path: string, status: Exclude<Status, 201> = 200, held = false, workspace = aiAlpha.id, cursor = "*"): AIControl {
    const gate = this.control(held);
    this.readReplies.set(`${workspace} ${path} ${cursor}`, { status, gate });
    return gate;
  }
  expectWrite(method: "POST" | "PATCH", path: string, status: Status = 200, held = false, errorMessage?: string): AIControl {
    const gate = this.control(held), key = `${method} ${path}`;
    this.writeReplies.set(key, [...this.writeReplies.get(key) ?? [], { status, gate, errorMessage }]);
    return gate;
  }
  discardUnsentWrite(control: AIControl) {
    if (control.call !== null) throw new Error("An arrived HTTP operation cannot be discarded.");
    for (const [key, replies] of this.writeReplies) this.writeReplies.set(key, replies.filter((reply) => reply.gate !== control));
    control.release();
  }
  async releaseResponses() {
    const arrived = [...this.gates].filter((gate) => gate.call !== null);
    for (const gate of this.gates) gate.release();
    await Promise.all(arrived.map((gate) => gate.delivered));
  }
  private error(status: number, message?: string) {
    const labels: Record<number, [string, string]> = {
      400: ["invalid-input", "The AI configuration request is invalid"],
      401: ["unauthorized", "Authentication is required"],
      403: ["forbidden", "This operation is not permitted"],
      404: ["not-found", "The requested resource was not found"],
      409: ["conflict", "Configuration changed. Review current profile and policy facts before approving again."],
      413: ["too-large", "The request exceeds the configured size limit"],
      429: ["unavailable", "The original combined 60-request budget was exceeded"],
      503: ["unavailable", "AI configuration is unavailable. Ask the operator to check server-side configuration."],
    };
    const [code, fallback] = labels[status];
    return { apiVersion, error: { code, message: message ?? fallback, requestId: "synthetic-ai-settings-request", retryable: false } };
  }
  private async deliver(route: Route, call: AICall, status: number, response: object, reply?: Reply) {
    call.status = status;
    call.response = structuredClone(response) as Record<string, unknown>;
    try {
      if (reply) { reply.gate.call = call; reply.gate.calls.push(call); await reply.gate.wait; }
      if (status === 401) { this.authenticated = false; this.settingsOpen = false; }
      await route.fulfill({ status, json: call.response });
    } finally { reply?.gate.complete(); }
  }
  private profileWrite(body: Record<string, unknown>, workspace: string, id?: string): AIProfile {
    const prior = id ? this.profiles.get(id)! : undefined;
    const profile: AIProfile = {
      id: id ?? nativeID("cb", ++this.sequence), workspaceId: workspace,
      name: String(body.name ?? prior?.name), family: (body.family ?? prior?.family) as Family,
      endpoint: String(body.endpoint ?? prior?.endpoint), model: String(body.model ?? prior?.model),
      deployment: String(body.deployment ?? prior?.deployment), enabled: (body.enabled ?? prior?.enabled) as boolean,
      structuredOutput: (body.structuredOutput ?? prior?.structuredOutput) as boolean,
      credentialConfigured: "apiKey" in body ? body.apiKey !== null : prior?.credentialConfigured ?? false,
      revision: prior?.revision ?? `profile:receipt/${this.sequence}`, createdAt: prior?.createdAt ?? changedAt,
      updatedAt: prior?.updatedAt ?? changedAt,
    };
    if (prior && (!isDeepStrictEqual(profile, prior) || typeof body.apiKey === "string")) {
      profile.revision = `profile-A:receipt/${++this.sequence}`;
      profile.updatedAt = changedAt;
    }
    this.profiles.set(profile.id, profile);
    return structuredClone(profile);
  }
  async assertPrivate(page: Page, cleared = false) {
    if (page.isClosed() || !page.url().startsWith("http://127.0.0.1:")) return;
    const state = await page.evaluate(() => ({
      text: document.body.textContent, cookie: document.cookie,
      storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
      attributes: [...document.querySelectorAll("*")].flatMap((element) => [...element.attributes]
        .filter((attribute) => !(element instanceof HTMLInputElement && element.type === "password" && attribute.name === "value"))
        .map((attribute) => [attribute.name, attribute.value])),
      fields: [...document.querySelectorAll("input,textarea")].map((element) => ({
        type: element.getAttribute("type"), value: (element as HTMLInputElement).value,
      })),
    }));
    const publicState = JSON.stringify({ ...state, fields: undefined }) + page.url() + this.consoleText.join("\n");
    for (const secret of canaries) for (const encoding of [secret, encodeURIComponent(secret), Buffer.from(secret).toString("base64")]) {
      expect(publicState, "No key/session/password in text, URL, readable cookie, browser storage or console.").not.toContain(encoding);
    }
    for (const field of state.fields.filter((field) => [draftKey, replacementKey].includes(field.value))) {
      expect(field.type).toBe("password");
      expect(cleared, "Secret drafts must be removed on close, success, session loss and workspace loss.").toBe(false);
    }
    for (const [key, value] of state.storage) { expect(key).toBe("aspm.theme"); expect(["light", "dark"]).toContain(value); }
  }
  async install(page: Page, origin: string) {
    if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(origin)) throw new Error("Only the existing loopback UI harness is authorized.");
    await page.context().addCookies([{ name: "aspm_session", value: aiCookie, url: origin, httpOnly: true, sameSite: "Lax" }]);
    page.on("pageerror", (error) => this.pageErrors.push(error.message));
    page.on("console", (message) => this.consoleText.push(message.text()));
    page.on("requestfailed", (request) => {
      const call = this.byRequest.get(request);
      if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.context().routeWebSocket("**/*", (socket) => {
      const url = new URL(socket.url());
      if (`http://${url.host}` !== origin || url.protocol !== "ws:" || url.pathname.startsWith("/api/")) {
        this.externalAttempts++; this.violations.push("Provider/native WebSocket traffic is forbidden.");
        socket.close(); return;
      }
      socket.connectToServer();
    });
    await page.context().route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url()), method = request.method(), path = url.pathname;
      if (url.origin !== origin) {
        this.externalAttempts++; this.violations.push("External/provider/native/evidence network traffic is forbidden.");
        await route.abort(); return;
      }
      if (canaries.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret)))) {
        this.violations.push("A synthetic secret appeared in a request URL."); await route.abort(); return;
      }
      if (!path.startsWith("/api/")) { await route.continue(); return; }
      const headers = await request.allHeaders();
      const call: AICall = {
        method, path, workspace: headers["x-aspm-workspace-id"], query: Object.fromEntries(url.searchParams),
        body: {}, status: null, response: null, failure: null,
      };
      this.requests.push(call); this.byRequest.set(request, call);
      if (this.requests.length > 60) {
        this.violations.push("The original combined 60-API-request per-case budget was exceeded.");
        await this.deliver(route, call, 429, this.error(429)); return;
      }
      if (headers.authorization || headers["x-aspm-bootstrap-token"] || headers["if-match"] || headers["x-aspm-revision"] ||
        Object.entries(headers).some(([key, value]) => key !== "cookie" && canaries.some((secret) => value.includes(secret)))) {
        this.violations.push("Use application cookie/workspace/Origin authority only; no provider/bootstrap/revision headers.");
      }
      const profileID = /^\/api\/v1\/ai\/profiles\/([a-f0-9]{32})$/.exec(path)?.[1];
      const grantID = /^\/api\/v1\/ai\/grants\/([a-f0-9]{32})$/.exec(path)?.[1];
      const revokeID = /^\/api\/v1\/ai\/grants\/([a-f0-9]{32})\/revoke$/.exec(path)?.[1];
      const write = method === "PATCH" && (profileID !== undefined || path === policyPath) ||
        method === "POST" && (revokeID !== undefined || [profilesPath, grantsPath, "/api/v1/login", "/api/v1/logout"].includes(path));
      const ai = path.startsWith("/api/v1/ai/");
      if (ai && !this.settingsOpen) this.violations.push("AI APIs were requested while the explicit AI settings entry was closed.");
      if (method !== "GET" && !write || write && url.search !== "") {
        this.violations.push(`Undeclared configuration operation: ${method} ${path}.`); await route.abort(); return;
      }
      if (write) {
        if (headers.origin !== origin) this.violations.push("Configuration writes require the matching Origin.");
        if (path !== "/api/v1/logout") {
          let body: unknown;
          try { body = request.postDataJSON(); } catch { body = null; }
          const media = headers["content-type"]?.split(";").map((part) => part.trim().toLowerCase());
          if (media?.[0] !== "application/json" || media.some((part) => part.startsWith("charset=") && part !== "charset=utf-8") ||
            headers["content-encoding"] && headers["content-encoding"] !== "identity" ||
            body === null || typeof body !== "object" || Array.isArray(body)) {
            this.violations.push("Only bounded UTF-8 JSON objects may cross the configuration boundary.");
            await this.deliver(route, call, 400, this.error(400)); return;
          }
          call.body = body as Record<string, unknown>;
          const allowed = profileID || path === profilesPath ? profileFields : path === policyPath ? ["mode"] :
            path === grantsPath ? grantFields : revokeID ? [] : ["email", "password"];
          if (Object.keys(call.body).some((key) => !allowed.includes(key))) {
            this.violations.push("No arbitrary approvalRef, actor, grant ID, key/headers or global allowlist fields.");
          }
          if ((request.postDataBuffer()?.length ?? 0) > 32 << 10) {
            this.violations.push("Configuration JSON exceeded 32 KiB.");
            await this.deliver(route, call, 413, this.error(413)); return;
          }
        }
      }
      const cookie = headers.cookie?.split(";").some((value) => value.trim() === `aspm_session=${aiCookie}`);
      const session = () => ({
        apiVersion, user: aiUser, expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        workspaces: workspaces.map((workspace) => ({ ...workspace, role: this.roles.get(workspace.id)! })),
      });
      if (method === "GET" && path === "/api/v1/session" && url.search === "") {
        if (this.authenticated && cookie) { await this.deliver(route, call, 200, session()); this.sessionCompleted = true; }
        else await this.deliver(route, call, 401, this.error(401));
        return;
      }
      if (method === "POST" && path === "/api/v1/login") {
        if (!isDeepStrictEqual(call.body, { email: aiUser.email, password: aiPassword })) {
          await this.deliver(route, call, 401, this.error(401)); return;
        }
        this.authenticated = true; this.settingsOpen = false; call.status = 200; call.response = session();
        await route.fulfill({ status: 200, json: call.response, headers: { "set-cookie": `aspm_session=${aiCookie}; Path=/; HttpOnly; SameSite=Lax` } });
        this.sessionCompleted = true; return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected traffic preceded verified session completion.");
      if (!this.authenticated || !cookie) { await this.deliver(route, call, 401, this.error(401)); return; }
      if (method === "POST" && path === "/api/v1/logout") {
        this.authenticated = false; this.sessionCompleted = false; this.settingsOpen = false; call.status = 204;
        await route.fulfill({ status: 204, headers: { "set-cookie": "aspm_session=; Max-Age=0; Path=/; HttpOnly; SameSite=Lax" } }); return;
      }
      const workspace = call.workspace;
      if (!workspace || !this.roles.has(workspace)) {
        this.violations.push("Protected metadata requires a selected verified-session workspace.");
        await this.deliver(route, call, 403, this.error(403)); return;
      }
      const reply = method === "GET"
        ? this.readReplies.get(`${workspace} ${path} ${url.searchParams.get("cursor") ?? ""}`) ?? this.readReplies.get(`${workspace} ${path} *`)
        : this.writeReplies.get(`${method} ${path}`)?.shift();
      if (ai && write && !reply) {
        this.violations.push("An AI write occurred without its declared explicit user action.");
        await route.abort(); return;
      }
      const denied = !this.serverRoles.has(workspace) || ai && write && this.serverRoles.get(workspace) !== "admin";
      if (denied) { await this.deliver(route, call, 403, this.error(403), reply); return; }
      try {
        if (method === "GET" && ["/api/v1/integrations/connections", "/api/v1/sources"].includes(path)) {
          pageParameters(url); await this.deliver(route, call, 200, { apiVersion, items: [], total: 0, nextCursor: null }); return;
        }
        if (method === "GET" && path === "/api/v1/integrations/catalog" && url.search === "") {
          await this.deliver(route, call, 200, catalogResponse); return;
        }
        if (!ai) throw new Error(`No undeclared native application API: ${method} ${path}`);
        if (method === "GET" && [profilesPath, grantsPath].includes(path)) {
          pageParameters(url);
          const values: Array<AIProfile | AIGrant> = path === profilesPath ? [...this.profiles.values()] : [...this.grants.values()];
          const response = pageOf(values.filter((value) => value.workspaceId === workspace), url);
          const status = reply?.status ?? 200;
          if (status === 200) this.pages.push({ call, response: structuredClone(response) });
          await this.deliver(route, call, status, status === 200 ? response : this.error(status, reply?.errorMessage), reply); return;
        }
        if (url.search !== "") throw new Error("Configuration detail/write has no query parameters.");
        if (profileID && this.profiles.get(profileID)?.workspaceId !== workspace ||
          (grantID || revokeID) && this.grants.get((grantID ?? revokeID)!)?.workspaceId !== workspace) {
          await this.deliver(route, call, 404, this.error(404), reply); return;
        }
        if (method === "GET" && (profileID || grantID || path === policyPath)) {
          const response = profileID ? { apiVersion, profile: this.profiles.get(profileID)! } :
            grantID ? { apiVersion, grant: this.grants.get(grantID)! } : { apiVersion, policy: this.policies.get(workspace)! };
          const status = reply?.status ?? 200;
          await this.deliver(route, call, status, status === 200 ? response : this.error(status, reply?.errorMessage), reply); return;
        }
        if (reply && reply.status >= 400) {
          await this.deliver(route, call, reply.status, this.error(reply.status, reply.errorMessage), reply); return;
        }
        if (method === "POST" && path === profilesPath || method === "PATCH" && profileID) {
          const prior = profileID ? this.profiles.get(profileID) : undefined;
          if (!profileBodyValid(call.body, prior)) { await this.deliver(route, call, 400, this.error(400), reply); return; }
          if (typeof call.body.apiKey === "string" && !this.encryptionAvailable) {
            await this.deliver(route, call, 503, this.error(503), reply); return;
          }
          const profile = this.profileWrite(call.body, workspace, profileID);
          await this.deliver(route, call, profileID ? 200 : 201, { apiVersion, profile }, reply); return;
        }
        if (method === "PATCH" && path === policyPath) {
          if (!exactKeys(call.body, ["mode"]) || !modes.includes(call.body.mode as Mode)) {
            await this.deliver(route, call, 400, this.error(400), reply); return;
          }
          const prior = this.policies.get(workspace)!;
          const policy = prior.mode === call.body.mode ? prior : {
            workspaceId: workspace, mode: call.body.mode as Mode, revision: `policy-A:receipt/${++this.sequence}`,
            updatedAt: changedAt, updatedBy: aiUser.id,
          };
          this.policies.set(workspace, policy);
          await this.deliver(route, call, 200, { apiVersion, policy }, reply); return;
        }
        if (method === "POST" && path === grantsPath) {
          const body = call.body;
          if (!exactKeys(body, grantFields) || !backendID(body.profileId) || typeof body.profileRevision !== "string" ||
            !body.profileRevision || typeof body.policyRevision !== "string" || !body.policyRevision ||
            typeof body.destination !== "string" || body.task !== "finding-validity" || body.dataClass !== "finding-evidence" ||
            !timestamp(body.expiresAt) || !body.expiresAt.endsWith("Z") || Date.parse(body.expiresAt) <= Date.now()) {
            await this.deliver(route, call, 400, this.error(400), reply); return;
          }
          const profile = this.profiles.get(body.profileId), policy = this.policies.get(workspace)!;
          if (!profile || profile.workspaceId !== workspace) { await this.deliver(route, call, 404, this.error(404), reply); return; }
          if (policy.mode !== "approved-hosted" || !profile.enabled || !profile.structuredOutput ||
            profile.revision !== body.profileRevision || policy.revision !== body.policyRevision || profile.endpoint !== body.destination) {
            await this.deliver(route, call, 409, this.error(409), reply); return;
          }
          const grant: AIGrant = {
            id: nativeID("db", ++this.sequence), workspaceId: workspace, profileId: profile.id, profileRevision: profile.revision,
            policyRevision: policy.revision, destination: profile.endpoint, task: "finding-validity", dataClass: "finding-evidence",
            expiresAt: body.expiresAt, createdAt: changedAt, grantedBy: aiUser.id, revokedAt: null, revokedBy: null,
          };
          this.grants.set(grant.id, grant);
          await this.deliver(route, call, 201, { apiVersion, grant }, reply); return;
        }
        if (method === "POST" && revokeID) {
          if (!exactKeys(call.body, [])) { await this.deliver(route, call, 400, this.error(400), reply); return; }
          const prior = this.grants.get(revokeID)!;
          const grant = prior.revokedAt === null ? { ...prior, revokedAt, revokedBy: aiUser.id } : prior;
          this.grants.set(revokeID, grant);
          await this.deliver(route, call, 200, { apiVersion, grant }, reply); return;
        }
        throw new Error(`Undeclared configuration route: ${method} ${path}`);
      } catch (error) {
        this.violations.push(String(error)); await route.abort();
      }
    });
  }
}

export const test = base.extend<{ ai: AISettingsHTTP }>({
  ai: async ({ page, baseURL }, use, testInfo) => {
    if (!baseURL) throw new Error("The existing browser server must supply baseURL.");
    const ai = new AISettingsHTTP();
    await ai.install(page, new URL(baseURL).origin);
    try { await use(ai); }
    finally {
      await ai.releaseResponses();
      const redact = (value: unknown) => {
        let json = JSON.stringify(value);
        for (const secret of canaries) json = json.replaceAll(secret, "[synthetic secret redacted]");
        return JSON.parse(json) as unknown;
      };
      await testInfo.attach("ai-http-boundary", { contentType: "application/json", body: JSON.stringify(redact({
        sourceWitnesses: sourceWitnesses(),
        reached: ai.reached, requests: ai.requests, combinedAPIRequests: ai.requests.length, aiRequests: ai.aiCalls().length,
        externalAttempts: ai.externalAttempts, violations: ai.violations, pageErrors: ai.pageErrors,
      }), null, 2) });
      expect.soft(ai.violations, "Only declared protocol-shaped synthetic HTTP may substitute for the service.").toEqual([]);
      expect.soft(ai.pageErrors, "No unhandled production browser exceptions.").toEqual([]);
      expect.soft(ai.externalAttempts, "No provider or native endpoint I/O.").toBe(0);
      expect.soft(ai.requests.length, "Original combined per-case API budget.").toBeLessThanOrEqual(60);
      await ai.assertPrivate(page);
    }
  },
});
export { expect } from "@playwright/test";
