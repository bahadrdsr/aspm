import { createHash } from "node:crypto";
import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import {
  apiVersion, approvalsPath, backendID, betaVerificationFinding, canonicalFixture,
  evidencePath, findingPath, initialApproval, initialEvidence, initialJob, jobsPath,
  verificationAlpha, verificationBeta, verificationCookie, verificationExpiresAt,
  verificationFinding, verificationID, verificationMethod, verificationRevocationRationale,
  verificationSchema, verificationUser, verificationWorkItem,
} from "./deterministic-verification-data";
import type {
  VerificationApproval, VerificationEvidence, VerificationJob, VerificationPage,
  VerificationRole, VerificationState,
} from "./deterministic-verification-data";

type Denial = 400 | 401 | 403 | 404 | 409 | 413 | 503;
type ReplyStatus = 200 | 201 | 202 | Denial;
export interface VerificationCall {
  method: string;
  path: string;
  workspace: string | undefined;
  query: Record<string, string>;
  body: Record<string, unknown>;
  status: number | null;
  response: Record<string, unknown> | null;
  failure: string | null;
}
export interface VerificationControl {
  call: VerificationCall | null;
  requested: Promise<void>;
  delivered: Promise<void>;
  release: () => void;
}
interface Gate extends VerificationControl {
  wait: Promise<void>;
  arrive: () => void;
  complete: () => void;
}
interface QueuedReply {
  status: ReplyStatus;
  gate: Gate;
  workspace: string;
  value?: Record<string, unknown>;
  abort?: boolean;
}

const fixtureSecrets = [verificationCookie, "synthetic-password-not-used"];

function nativePage<T extends { id: string }>(items: T[], url: URL): VerificationPage<T> {
  if ([...url.searchParams.keys()].some((key) => !["limit", "cursor"].includes(key)) ||
    url.searchParams.getAll("limit").length > 1 || url.searchParams.getAll("cursor").length > 1) {
    throw new Error("Verification history accepts only one native limit and cursor.");
  }
  const limitText = url.searchParams.get("limit") ?? "";
  const cursor = url.searchParams.get("cursor") ?? "";
  if (!/^(?:[1-9]|[1-9][0-9]|100)$/.test(limitText) || cursor !== "" && !backendID(cursor)) {
    throw new Error("Verification history requires limit 1..100 and an optional lower-case native cursor.");
  }
  const limit = Number(limitText);
  const sorted = [...items].filter((item) => item.id > cursor).sort((left, right) => left.id.localeCompare(right.id));
  return {
    apiVersion, dataOrigin: "synthetic", items: structuredClone(sorted.slice(0, limit)),
    total: items.length, nextCursor: sorted.length > limit ? sorted[limit - 1].id : null,
  };
}

function exactKeys(value: Record<string, unknown>, keys: string[]) {
  return Object.keys(value).sort().join(",") === [...keys].sort().join(",");
}

function nonblank(value: unknown, limit: number) {
  return typeof value === "string" && value.trim() !== "" && !value.includes("\0") &&
    Buffer.byteLength(value, "utf8") <= limit;
}

export class DeterministicVerificationAPI {
  authenticated = true;
  readonly roles = new Map<string, VerificationRole>([
    [verificationAlpha.id, "admin"], [verificationBeta.id, "viewer"],
  ]);
  readonly serverRoles = new Map(this.roles);
  readonly findings = new Map([
    [verificationFinding.id, structuredClone(verificationFinding)],
    [betaVerificationFinding.id, structuredClone(betaVerificationFinding)],
  ]);
  readonly evidence = new Map<string, VerificationEvidence>([[initialEvidence.id, structuredClone(initialEvidence)]]);
  readonly approvals = new Map<string, VerificationApproval>([[initialApproval.id, structuredClone(initialApproval)]]);
  readonly jobs = new Map<string, VerificationJob>([[initialJob.id, structuredClone(initialJob)]]);
  readonly requests: VerificationCall[] = [];
  readonly jobPages: Array<{ call: VerificationCall; response: VerificationPage<VerificationJob> }> = [];
  readonly violations: string[] = [];
  readonly pageErrors: string[] = [];
  readonly consoleText: string[] = [];
  externalAttempts = 0;
  private replies = new Map<string, QueuedReply[]>();
  private gates = new Set<Gate>();
  private byRequest = new Map<Request, VerificationCall>();
  private idempotency = new Map<string, string>([["synthetic-initial-job", initialJob.id]]);
  private sessionCompleted = false;
  private sequence = 1000;

  calls(method = "GET", path = jobsPath) {
    return this.requests.filter((call) => call.method === method && call.path === path);
  }

  seedJobs(count: number) {
    this.jobs.clear();
    for (let index = 1; index <= count; index += 1) {
      const state: VerificationState = index % 5 === 0 ? "succeeded" : "queued";
      const item = structuredClone(initialJob);
      Object.assign(item, {
        id: verificationID("91", index), state,
        createdAt: new Date(Date.parse("2026-10-09T09:32:00Z") + index).toISOString(),
        completedAt: state === "succeeded" ? new Date(Date.parse("2026-10-09T09:33:00Z") + index).toISOString() : null,
        result: state === "succeeded" ? {
          method: verificationMethod, environmentId: initialEvidence.environmentId,
          scopeRevision: initialEvidence.scopeRevision, evidenceId: initialEvidence.id,
          evidenceDigest: initialEvidence.digest,
          outcome: index % 2 ? "reproduced" : "not-reproduced",
          closeFinding: false, falsePositive: false,
        } : null,
        failure: null,
      } satisfies Partial<VerificationJob>);
      this.jobs.set(item.id, item);
    }
  }

  setJobState(id: string, state: VerificationState) {
    const item = this.jobs.get(id);
    if (!item) throw new Error(`Unknown synthetic verification job ${id}.`);
    item.state = state;
    item.completedAt = ["succeeded", "blocked", "failed", "cancelled"].includes(state) ?
      "2026-10-09T09:40:00Z" : null;
    item.result = state === "succeeded" ? {
      method: verificationMethod, environmentId: item.environmentId,
      scopeRevision: item.scopeRevision, evidenceId: item.evidenceId,
      evidenceDigest: item.evidenceDigest, outcome: "reproduced",
      closeFinding: false, falsePositive: false,
    } : null;
    item.failure = ["blocked", "failed", "cancelled"].includes(state) ? {
      code: state === "blocked" ? "approval-expired" : state === "cancelled" ? "approval-revoked" : "fixture-format",
      message: `Synthetic ${state} deterministic verification diagnostic.`,
      retryable: false,
    } : null;
  }

  private queue(method: string, path: string, status: ReplyStatus, held: boolean,
    workspace: string, value?: Record<string, unknown>, abort = false) {
    let release!: () => void, arrive!: () => void, complete!: () => void;
    const gate: Gate = {
      call: null,
      wait: new Promise<void>((resolve) => { release = resolve; }),
      requested: new Promise<void>((resolve) => { arrive = resolve; }),
      delivered: new Promise<void>((resolve) => { complete = resolve; }),
      release: () => release(), arrive: () => arrive(), complete: () => complete(),
    };
    const key = `${method} ${path}`;
    this.replies.set(key, [...(this.replies.get(key) ?? []), { status, gate, workspace, value, abort }]);
    this.gates.add(gate);
    if (!held) release();
    return gate;
  }

  queueJobHistory(status: 200 | Denial = 200, held = false, value?: Record<string, unknown>,
    workspace = verificationAlpha.id) {
    return this.queue("GET", jobsPath, status, held, workspace, value);
  }

  queueJobDetail(id: string, status: 200 | Denial = 200, held = false,
    value?: Record<string, unknown>, workspace = verificationAlpha.id) {
    return this.queue("GET", `${jobsPath}/${id}`, status, held, workspace, value);
  }

  loseNextQueueAcknowledgement(held = false) {
    return this.queue("POST", jobsPath, 202, held, verificationAlpha.id, undefined, true);
  }

  async releaseResponses() {
    const arrived = [...this.gates].filter((gate) => gate.call !== null);
    for (const gate of this.gates) gate.release();
    await Promise.all(arrived.map((gate) => gate.delivered));
  }

  private error(status: number) {
    const values: Record<number, [string, string]> = {
      400: ["invalid-input", "The synthetic verification request is invalid."],
      401: ["unauthorized", "Authentication is required."],
      403: ["forbidden", "Synthetic verification access denied."],
      404: ["not-found", "Synthetic verification record not found."],
      409: ["conflict", "Synthetic verification authority or idempotency binding changed."],
      413: ["too-large", "The synthetic verification fixture exceeds 64 KiB."],
      503: ["unavailable", "Synthetic verification service unavailable."],
    };
    const [code, message] = values[status];
    return { apiVersion, error: { code, message, requestId: "synthetic-verification-request", retryable: false } };
  }

  private async respond(route: Route, call: VerificationCall, status: number,
    value: Record<string, unknown>, reply?: QueuedReply) {
    call.status = status;
    call.response = structuredClone(value);
    try {
      if (reply) {
        reply.gate.call = call;
        reply.gate.arrive();
        await reply.gate.wait;
      }
      if (status === 401) this.authenticated = false;
      if (reply?.abort) {
        await route.abort("failed");
        return;
      }
      await route.fulfill({ status, json: call.response });
    } finally {
      reply?.gate.complete();
      if (reply) this.gates.delete(reply.gate);
    }
  }

  private async queued(route: Route, call: VerificationCall) {
    const queue = this.replies.get(`${call.method} ${call.path}`) ?? [];
    const reply = queue.shift();
    if (!reply) return undefined;
    if (reply.workspace !== call.workspace) {
      this.violations.push("A held verification response was delivered to a different workspace.");
      await this.respond(route, call, 400, this.error(400), reply);
      return null;
    }
    return reply;
  }

  async assertPrivate(page: Page) {
    if (page.isClosed()) return;
    const state = await page.evaluate(async () => ({
      text: document.body.textContent,
      html: document.documentElement.outerHTML,
      cookie: document.cookie,
      storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
      databases: typeof indexedDB.databases === "function" ?
        (await indexedDB.databases()).map((item) => item.name ?? "") : [],
    }));
    const combined = JSON.stringify(state) + page.url() + this.consoleText.join("\n");
    expect(combined).not.toContain(canonicalFixture);
    for (const secret of fixtureSecrets) expect(combined).not.toContain(secret);
    for (const [key, value] of state.storage) {
      expect(key, "Only the nonsecret theme preference may use browser storage.").toBe("aspm.theme");
      expect(["light", "dark"]).toContain(value);
    }
    expect(state.databases).toEqual([]);
  }

  async install(page: Page, origin: string) {
    const local = new URL(origin);
    if (local.protocol !== "http:" || local.hostname !== "127.0.0.1") {
      throw new Error("Deterministic verification fixture requires the existing loopback runner.");
    }
    await page.context().addCookies([{
      name: "aspm_session", value: verificationCookie, url: origin,
      httpOnly: true, sameSite: "Lax",
    }]);
    page.on("pageerror", (error) => this.pageErrors.push(error.message));
    page.on("console", (message) => this.consoleText.push(message.text()));
    page.on("requestfailed", (request) => {
      const call = this.byRequest.get(request);
      if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.route("**/*", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== origin) {
        this.externalAttempts += 1;
        this.violations.push("No target, provider, scanner, browser automation or external verification HTTP is authorized.");
        await route.abort();
        return;
      }
      if (!url.pathname.startsWith("/api/")) {
        await route.continue();
        return;
      }
      const headers = await request.allHeaders();
      const call: VerificationCall = {
        method: request.method(), path: url.pathname, workspace: headers["x-aspm-workspace-id"],
        query: Object.fromEntries(url.searchParams), body: {}, status: null, response: null, failure: null,
      };
      this.requests.push(call);
      this.byRequest.set(request, call);
      if (this.requests.length > 80) {
        this.violations.push("Deterministic verification exceeded the bounded 80-call browser budget.");
        await this.respond(route, call, 503, this.error(503));
        return;
      }
      if (headers.authorization || headers["x-aspm-bootstrap-token"] ||
        fixtureSecrets.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret)))) {
        this.violations.push("Verification must use only the HttpOnly session and never URL or bearer credentials.");
      }
      const write = call.method === "POST" &&
        (call.path === evidencePath || call.path === approvalsPath || call.path === jobsPath ||
          /^\/api\/v1\/findings\/[a-f0-9]{32}\/verification\/approvals\/[a-f0-9]{32}\/revoke$/.test(call.path));
      if (write) {
        if (headers.origin !== origin) this.violations.push("Verification writes require the matching Origin.");
        let parsed: unknown;
        try { parsed = request.postDataJSON(); } catch { parsed = null; }
        if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed) ||
          headers["content-type"]?.split(";")[0].trim().toLowerCase() !== "application/json") {
          await this.respond(route, call, 400, this.error(400));
          return;
        }
        call.body = parsed as Record<string, unknown>;
        if ((request.postDataBuffer()?.length ?? 0) > 64 << 10) {
          await this.respond(route, call, 413, this.error(413));
          return;
        }
      } else if (call.method !== "GET") {
        this.violations.push(`Undeclared deterministic verification operation: ${call.method} ${call.path}.`);
        await route.abort();
        return;
      }

      const cookie = headers.cookie?.split(";").some((part) => part.trim() === `aspm_session=${verificationCookie}`);
      const session = () => ({
        apiVersion, user: verificationUser, expiresAt: "2026-10-10T12:00:00Z",
        workspaces: [verificationAlpha, verificationBeta].map((workspace) => ({
          ...workspace, role: this.roles.get(workspace.id)!,
        })),
      });
      if (call.method === "GET" && call.path === "/api/v1/session" && url.search === "") {
        if (this.authenticated && cookie) {
          await this.respond(route, call, 200, session());
          this.sessionCompleted = true;
        } else await this.respond(route, call, 401, this.error(401));
        return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected verification data was requested before session completion.");
      if (!this.authenticated || !cookie) {
        await this.respond(route, call, 401, this.error(401));
        return;
      }
      const workspace = call.workspace;
      if (!workspace || !this.roles.has(workspace)) {
        await this.respond(route, call, 403, this.error(403));
        return;
      }
      const reply = await this.queued(route, call);
      if (reply === null) return;
      if (!this.serverRoles.has(workspace)) {
        await this.respond(route, call, 403, this.error(403), reply);
        return;
      }
      const role = this.serverRoles.get(workspace)!;

      if (call.method === "GET" && call.path === "/api/v1/work") {
        const items = [...this.findings.values()].filter((item) => item.workspaceId === workspace)
          .map((item) => verificationWorkItem(item));
        await this.respond(route, call, 200, { apiVersion, dataOrigin: "synthetic", items, total: items.length, nextCursor: null }, reply);
        return;
      }
      if (call.method === "GET" && call.path === findingPath) {
        const finding = this.findings.get(verificationFinding.id);
        if (!finding || finding.workspaceId !== workspace) {
          await this.respond(route, call, 404, this.error(404), reply);
        } else {
          await this.respond(route, call, 200, { apiVersion, dataOrigin: "synthetic", finding: structuredClone(finding) }, reply);
        }
        return;
      }
      if (call.method === "GET" && call.path === `${findingPath}/deliveries`) {
        await this.respond(route, call, 200, { apiVersion, dataOrigin: "synthetic", items: [], total: 0, nextCursor: null }, reply);
        return;
      }
      if (![evidencePath, approvalsPath, jobsPath].some((path) => call.path === path || call.path.startsWith(path + "/"))) {
        this.violations.push(`Undeclared deterministic verification endpoint: ${call.method} ${call.path}.`);
        await this.respond(route, call, 404, this.error(404), reply);
        return;
      }
      if (workspace !== verificationAlpha.id) {
        await this.respond(route, call, 404, this.error(404), reply);
        return;
      }

      if (call.method === "GET" && call.path === evidencePath) {
        let value: Record<string, unknown>;
        try { value = nativePage([...this.evidence.values()], url) as unknown as Record<string, unknown>; }
        catch { value = this.error(400); await this.respond(route, call, 400, value, reply); return; }
        await this.respond(route, call, reply?.status ?? 200, reply?.value ?? value, reply);
        return;
      }
      if (call.method === "GET" && call.path === approvalsPath) {
        for (const item of this.approvals.values()) {
          item.current = item.revokedAt === null && Date.parse(item.expiresAt) > Date.parse("2026-10-09T11:25:24Z");
        }
        let value: Record<string, unknown>;
        try { value = nativePage([...this.approvals.values()], url) as unknown as Record<string, unknown>; }
        catch { value = this.error(400); await this.respond(route, call, 400, value, reply); return; }
        await this.respond(route, call, reply?.status ?? 200, reply?.value ?? value, reply);
        return;
      }
      if (call.method === "GET" && call.path === jobsPath) {
        let value: VerificationPage<VerificationJob>;
        try { value = nativePage([...this.jobs.values()], url); }
        catch { await this.respond(route, call, 400, this.error(400), reply); return; }
        this.jobPages.push({ call, response: structuredClone(value) });
        await this.respond(route, call, reply?.status ?? 200,
          reply?.value ?? value as unknown as Record<string, unknown>, reply);
        return;
      }
      const detailID = new RegExp(`^${jobsPath}/([a-f0-9]{32})$`).exec(call.path)?.[1];
      if (call.method === "GET" && detailID) {
        const item = this.jobs.get(detailID);
        const status = reply?.status ?? (item ? 200 : 404);
        await this.respond(route, call, status,
          reply?.value ?? (status === 200 ? { apiVersion, dataOrigin: "synthetic", verification: structuredClone(item) } : this.error(status)), reply);
        return;
      }

      if (call.method === "POST" && call.path === evidencePath) {
        if (!["admin", "analyst"].includes(role)) {
          await this.respond(route, call, 403, this.error(403), reply);
          return;
        }
        const fixture = call.body.fixture;
        if (!exactKeys(call.body, ["method", "environmentId", "scopeRevision", "fixture"]) ||
          call.body.method !== verificationMethod || !nonblank(call.body.environmentId, 256) ||
          !nonblank(call.body.scopeRevision, 256) || fixture === null ||
          typeof fixture !== "object" || Array.isArray(fixture) ||
          !exactKeys(fixture as Record<string, unknown>, ["schema", "environmentId", "condition"]) ||
          (fixture as Record<string, unknown>).schema !== verificationSchema ||
          (fixture as Record<string, unknown>).environmentId !== call.body.environmentId ||
          typeof (fixture as Record<string, unknown>).condition !== "boolean") {
          await this.respond(route, call, 400, this.error(400), reply);
          return;
        }
        const canonical = JSON.stringify({
          schema: verificationSchema, environmentId: call.body.environmentId,
          condition: (fixture as Record<string, unknown>).condition,
        });
        const item: VerificationEvidence = {
          id: verificationID("72", ++this.sequence), workspaceId: workspace,
          findingId: verificationFinding.id, submittedBy: verificationUser.id,
          method: verificationMethod, schema: verificationSchema,
          environmentId: String(call.body.environmentId), scopeRevision: String(call.body.scopeRevision),
          findingEvidenceRevision: 3,
          digest: `sha256:${createHash("sha256").update(canonical).digest("hex")}`,
          sizeBytes: Buffer.byteLength(canonical, "utf8"), createdAt: "2026-10-09T11:26:00Z",
        };
        this.evidence.set(item.id, item);
        await this.respond(route, call, 201, { apiVersion, dataOrigin: "synthetic", evidence: structuredClone(item) }, reply);
        return;
      }
      if (call.method === "POST" && call.path === approvalsPath) {
        if (role !== "admin") {
          await this.respond(route, call, 403, this.error(403), reply);
          return;
        }
        const evidence = typeof call.body.evidenceId === "string" ? this.evidence.get(call.body.evidenceId) : undefined;
        if (!exactKeys(call.body, ["evidenceId", "rationale", "expiresAt"]) || !evidence ||
          !nonblank(call.body.rationale, 8192) || call.body.expiresAt !== verificationExpiresAt) {
          await this.respond(route, call, 400, this.error(400), reply);
          return;
        }
        const item: VerificationApproval = {
          id: verificationID("82", ++this.sequence), workspaceId: workspace,
          findingId: verificationFinding.id, evidenceId: evidence.id,
          approvedBy: verificationUser.id, method: verificationMethod,
          environmentId: evidence.environmentId, scopeRevision: evidence.scopeRevision,
          findingEvidenceRevision: evidence.findingEvidenceRevision, evidenceDigest: evidence.digest,
          rationale: String(call.body.rationale), createdAt: "2026-10-09T11:27:00Z",
          expiresAt: verificationExpiresAt, revokedAt: null, revokedBy: null,
          revocationRationale: null, current: true,
        };
        this.approvals.set(item.id, item);
        await this.respond(route, call, 201, { apiVersion, dataOrigin: "synthetic", approval: structuredClone(item) }, reply);
        return;
      }
      const revokeID = new RegExp(`^${approvalsPath}/([a-f0-9]{32})/revoke$`).exec(call.path)?.[1];
      if (call.method === "POST" && revokeID) {
        if (role !== "admin") {
          await this.respond(route, call, 403, this.error(403), reply);
          return;
        }
        const approval = this.approvals.get(revokeID);
        if (!approval) {
          await this.respond(route, call, 404, this.error(404), reply);
          return;
        }
        if (!exactKeys(call.body, ["rationale"]) || call.body.rationale !== verificationRevocationRationale ||
          approval.revokedAt !== null) {
          await this.respond(route, call, 409, this.error(409), reply);
          return;
        }
        approval.revokedAt = "2026-10-09T11:28:00Z";
        approval.revokedBy = verificationUser.id;
        approval.revocationRationale = verificationRevocationRationale;
        approval.current = false;
        for (const item of this.jobs.values()) {
          if (item.approvalId === approval.id && ["queued", "processing"].includes(item.state)) this.setJobState(item.id, "cancelled");
        }
        await this.respond(route, call, 200, { apiVersion, dataOrigin: "synthetic", approval: structuredClone(approval) }, reply);
        return;
      }
      if (call.method === "POST" && call.path === jobsPath) {
        if (!["admin", "analyst"].includes(role)) {
          await this.respond(route, call, 403, this.error(403), reply);
          return;
        }
        if (!exactKeys(call.body, ["approvalId", "idempotencyKey"]) ||
          !backendID(call.body.approvalId) || !nonblank(call.body.idempotencyKey, 256)) {
          await this.respond(route, call, 400, this.error(400), reply);
          return;
        }
        const approval = this.approvals.get(call.body.approvalId);
        if (!approval?.current) {
          await this.respond(route, call, 409, this.error(409), reply);
          return;
        }
        const key = String(call.body.idempotencyKey);
        const existingID = this.idempotency.get(key);
        if (existingID) {
          const existing = this.jobs.get(existingID)!;
          if (existing.approvalId !== approval.id) {
            await this.respond(route, call, 409, this.error(409), reply);
          } else {
            await this.respond(route, call, 200,
              { apiVersion, dataOrigin: "synthetic", verification: structuredClone(existing) }, reply);
          }
          return;
        }
        const item: VerificationJob = {
          id: verificationID("92", ++this.sequence), workspaceId: workspace,
          findingId: verificationFinding.id, approvalId: approval.id,
          evidenceId: approval.evidenceId, requestedBy: verificationUser.id,
          method: verificationMethod, environmentId: approval.environmentId,
          scopeRevision: approval.scopeRevision,
          findingEvidenceRevision: approval.findingEvidenceRevision,
          evidenceDigest: approval.evidenceDigest, state: "queued",
          createdAt: "2026-10-09T11:29:00Z", completedAt: null, failure: null, result: null,
        };
        this.jobs.set(item.id, item);
        this.idempotency.set(key, item.id);
        await this.respond(route, call, 202,
          { apiVersion, dataOrigin: "synthetic", verification: structuredClone(item) }, reply);
        return;
      }
      this.violations.push(`Unhandled deterministic verification endpoint: ${call.method} ${call.path}.`);
      await this.respond(route, call, 404, this.error(404), reply);
    });
  }
}

export const test = base.extend<{ verificationAPI: DeterministicVerificationAPI }>({
  verificationAPI: async ({ page, baseURL }, use, testInfo) => {
    if (!baseURL) throw new Error("Deterministic verification requires the unchanged loopback runner.");
    const verificationAPI = new DeterministicVerificationAPI();
    await verificationAPI.install(page, new URL(baseURL).origin);
    try {
      await use(verificationAPI);
    } finally {
      await verificationAPI.releaseResponses();
      expect.soft(verificationAPI.violations,
        "Only declared same-origin deterministic fixture operations are permitted.").toEqual([]);
      expect.soft(verificationAPI.pageErrors).toEqual([]);
      expect.soft(verificationAPI.externalAttempts).toBe(0);
      await verificationAPI.assertPrivate(page);
      await testInfo.attach("deterministic-verification-http-ledger", {
        body: JSON.stringify({
          requests: verificationAPI.requests.map((call) => call.path === evidencePath && call.method === "POST" ?
            { ...call, body: { ...call.body, fixture: "redacted exact synthetic fixture object" } } : call),
          violations: verificationAPI.violations, pageErrors: verificationAPI.pageErrors,
          externalAttempts: verificationAPI.externalAttempts,
          boundary: "Synthetic fixture data only. No target, browser automation, provider, scanner, shell, subprocess or external request.",
        }, null, 2),
        contentType: "application/json",
      });
    }
  },
});

export { expect } from "@playwright/test";
