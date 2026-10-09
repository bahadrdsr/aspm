import { expect, test as base } from "@playwright/test";
import type { Page, Request, Route } from "@playwright/test";
import { apiVersion } from "./api-contract";
import { bootstrapToken, password, sessionCookie, wrongPassword } from "./application-fixture";
import { catalogResponse, findingResponse, workResponse } from "./fixtures";
import { emptySlackNavigation } from "./slack-navigation";
import {
  alphaOverview, alphaSLAPolicy, betaOverview, betaSLAPolicy, coverageAssetsPath, coverageParameters,
  coverageStorageCanaries, findingMetricParameters, findingMetricsPath, findingMetricStorageCanaries,
  noSLAQuery, overviewDays, overviewPath, reportAlpha, reportBeta, reportUser, savedReport, savedSnapshots,
  snapshotParameters, snapshotSummary, snapshotsPath, syntheticFindingMetricResponse, syntheticTrendResponse,
  slaFindingsPath, slaFindingParameters, slaPath, slaPolicyPath, slaStorageCanaries, syntheticCoverageResponse,
  syntheticSLAFindingPage, syntheticSLAPolicyResponse, syntheticSLAResponse, trendDays, trendsPath,
  trendStorageCanaries, withFreshness,
} from "./reports-data";
import type {
  CoverageState, FindingMetric, ReportRole, SLAStatus, SnapshotPage, SnapshotState,
  SyntheticCoverageDrilldown, SyntheticFindingMetricDrilldown, SyntheticPostureReport,
  SyntheticSLAResponse, SyntheticSLAFindingPage, SyntheticSLAPolicy, SyntheticSnapshot, SyntheticTrend,
} from "./reports-data";

type Denial = 400 | 401 | 403 | 404 | 409 | 413 | 503;
type Reply<T> = { status: 200; value: T } | { status: Denial };
export interface ReportCall {
  method: string; path: string; workspace: string | undefined; query: Record<string, string>;
  body: Record<string, unknown>; reportAsOf: string | undefined; failure: string | null;
}
export interface ReportResponseControl {
  requested: Promise<void>;
  delivered: Promise<void>;
  release: () => void;
  call: ReportCall | null;
}
interface Gate extends ReportResponseControl {
  wait: Promise<void>;
  arrive: () => void;
  complete: () => void;
}
interface Scheduled<T> { reply: Reply<T>; gate: Gate; commitPolicy?: SyntheticSLAPolicy }
const secretCanaries = [password, wrongPassword, bootstrapToken, sessionCookie];

export class ReportsAPI {
  authenticated = true;
  readonly roles = new Map<string, ReportRole>([[reportAlpha.id, "admin"], [reportBeta.id, "analyst"]]);
  readonly serverRoles = new Map<string, ReportRole>([[reportAlpha.id, "admin"], [reportBeta.id, "analyst"]]);
  readonly requests: ReportCall[] = [];
  readonly violations: string[] = [];
  readonly pages: Array<{ call: ReportCall; response: SnapshotPage }> = [];
  readonly snapshots = new Map<string, SyntheticSnapshot>();
  readonly overviews = new Map([[reportAlpha.id, alphaOverview], [reportBeta.id, betaOverview]]);
  readonly slaPolicies = new Map<string, SyntheticSLAPolicy>([
    [reportAlpha.id, structuredClone(alphaSLAPolicy)],
    [reportBeta.id, structuredClone(betaSLAPolicy)],
  ]);
  private sessionCompleted = false;
  private createdCount = 0;
  private gates = new Set<Gate>();
  private overviewReplies = new Map<string, Scheduled<SyntheticPostureReport>[]>();
  private historyReplies = new Map<string, Scheduled<SnapshotPage>[]>();
  private historyHolds = new Map<string, Gate[]>();
  private snapshotReplies = new Map<string, Scheduled<SyntheticSnapshot>[]>();
  private trendReplies = new Map<string, Scheduled<unknown>[]>();
  private coverageReplies = new Map<string, Scheduled<unknown>[]>();
  private findingMetricReplies = new Map<string, Scheduled<unknown>[]>();
  private slaSummaryReplies = new Map<string, Scheduled<unknown>[]>();
  private slaFindingReplies = new Map<string, Scheduled<unknown>[]>();
  private slaPolicyReplies = new Map<string, Scheduled<unknown>[]>();
  private slaPolicyUpdateReplies = new Map<string, Scheduled<unknown>[]>();
  private createGates: Gate[] = [];
  private callsByRequest = new Map<Request, ReportCall>();

  constructor() {
    this.seedHistory(reportAlpha.id, 2);
    this.seedHistory(reportBeta.id, 2);
  }

  calls(method: string, path: string) { return this.requests.filter((call) => call.method === method && call.path === path); }

  seedHistory(workspace: string, count: number) {
    for (const [id, snapshot] of this.snapshots) if (snapshot.workspaceId === workspace) this.snapshots.delete(id);
    for (const snapshot of savedSnapshots(workspace, count)) this.snapshots.set(snapshot.id, snapshot);
  }

  private gate(held: boolean): Gate {
    let release!: () => void, arrive!: () => void, complete!: () => void;
    const wait = new Promise<void>((resolve) => { release = resolve; });
    const requested = new Promise<void>((resolve) => { arrive = resolve; });
    const delivered = new Promise<void>((resolve) => { complete = resolve; });
    const gate = { wait, requested, delivered, release, arrive, complete, call: null };
    this.gates.add(gate);
    if (!held) release();
    return gate;
  }

  private queue<T>(queue: Map<string, Scheduled<T>[]>, key: string, reply: Reply<T>, held: boolean): ReportResponseControl {
    const gate = this.gate(held);
    queue.set(key, [...(queue.get(key) ?? []), { reply: structuredClone(reply), gate }]);
    return gate;
  }

  queueOverview(workspace: string, reply: Reply<SyntheticPostureReport>, held = false) {
    if (!this.roles.has(workspace) || reply.status === 200 && reply.value.workspaceId !== workspace) {
      throw new Error("Queued overview must belong to its known synthetic workspace.");
    }
    return this.queue(this.overviewReplies, workspace, reply, held);
  }

  queueHistory(workspace: string, reply: Reply<SnapshotPage>, held = false) {
    if (!this.roles.has(workspace) || reply.status === 200 && reply.value.items.some((item) => item.workspaceId !== workspace)) {
      throw new Error("Queued history must belong to its known synthetic workspace.");
    }
    return this.queue(this.historyReplies, workspace, reply, held);
  }

  holdHistory(workspace: string): ReportResponseControl {
    if (!this.roles.has(workspace)) throw new Error("History holds require a known synthetic workspace.");
    const gate = this.gate(true);
    this.historyHolds.set(workspace, [...(this.historyHolds.get(workspace) ?? []), gate]);
    return gate;
  }

  queueSnapshot(id: string, reply: Reply<SyntheticSnapshot>, held = false) {
    const snapshot = this.snapshots.get(id);
    if (!snapshot || reply.status === 200 &&
      (reply.value.id !== id || reply.value.workspaceId !== snapshot.workspaceId ||
        snapshot.state === "succeeded" && JSON.stringify(reply.value) !== JSON.stringify(snapshot))) {
      throw new Error("Queued detail requires a known snapshot and cannot rewrite an immutable completed result.");
    }
    return this.queue(this.snapshotReplies, id, reply, held);
  }

  queueTrend(workspace: string, reply: Reply<SyntheticTrend>, held = false) {
    if (!this.roles.has(workspace) || reply.status === 200 && reply.value.workspaceId !== workspace) {
      throw new Error("Queued historical trends must belong to their known synthetic workspace.");
    }
    const wrapped: Reply<unknown> = reply.status === 200
      ? { status: 200, value: { apiVersion, dataOrigin: "live", trend: structuredClone(reply.value) } }
      : reply;
    return this.queue(this.trendReplies, workspace, wrapped, held);
  }

  queueTrendRaw(workspace: string, value: unknown, held = false) {
    if (!this.roles.has(workspace)) throw new Error("Raw historical trend replies require a known synthetic workspace.");
    return this.queue(this.trendReplies, workspace, { status: 200, value: structuredClone(value) }, held);
  }

  private coverageKey(workspace: string, state: CoverageState) { return `${workspace}:${state}`; }

  queueCoverage(workspace: string, state: CoverageState, reply: Reply<SyntheticCoverageDrilldown>, held = false) {
    if (!this.roles.has(workspace) || reply.status === 200 &&
      (reply.value.workspaceId !== workspace || reply.value.state !== state)) {
      throw new Error("Queued coverage membership must belong to its known synthetic workspace and state.");
    }
    const wrapped: Reply<unknown> = reply.status === 200
      ? { status: 200, value: { apiVersion, dataOrigin: "live", drilldown: structuredClone(reply.value) } }
      : reply;
    return this.queue(this.coverageReplies, this.coverageKey(workspace, state), wrapped, held);
  }

  queueCoverageRaw(workspace: string, state: CoverageState, value: unknown, held = false) {
    if (!this.roles.has(workspace)) throw new Error("Raw coverage replies require a known synthetic workspace.");
    return this.queue(this.coverageReplies, this.coverageKey(workspace, state),
      { status: 200, value: structuredClone(value) }, held);
  }

  private findingMetricKey(workspace: string, metric: FindingMetric) { return `${workspace}:${metric}`; }

  queueFindingMetric(workspace: string, metric: FindingMetric,
    reply: Reply<SyntheticFindingMetricDrilldown>, held = false) {
    if (!this.roles.has(workspace) || reply.status === 200 &&
      (reply.value.workspaceId !== workspace || reply.value.metric !== metric)) {
      throw new Error("Queued finding membership must belong to its known synthetic workspace and metric.");
    }
    const wrapped: Reply<unknown> = reply.status === 200
      ? { status: 200, value: { apiVersion, dataOrigin: "live", drilldown: structuredClone(reply.value) } }
      : reply;
    return this.queue(this.findingMetricReplies, this.findingMetricKey(workspace, metric), wrapped, held);
  }

  queueFindingMetricRaw(workspace: string, metric: FindingMetric, value: unknown, held = false) {
    if (!this.roles.has(workspace)) throw new Error("Raw finding metric replies require a known synthetic workspace.");
    return this.queue(this.findingMetricReplies, this.findingMetricKey(workspace, metric),
      { status: 200, value: structuredClone(value) }, held);
  }

  queueSLA(workspace: string, reply: Reply<SyntheticSLAResponse["sla"]>, held = false) {
    if (!this.roles.has(workspace) || reply.status === 200 && reply.value.workspaceId !== workspace) {
      throw new Error("Queued SLA summary must belong to its known synthetic workspace.");
    }
    const wrapped: Reply<unknown> = reply.status === 200
      ? { status: 200, value: { apiVersion, dataOrigin: "live", sla: structuredClone(reply.value) } }
      : reply;
    return this.queue(this.slaSummaryReplies, workspace, wrapped, held);
  }

  queueSLARaw(workspace: string, value: unknown, held = false) {
    if (!this.roles.has(workspace)) throw new Error("Raw SLA summaries require a known synthetic workspace.");
    return this.queue(this.slaSummaryReplies, workspace,
      { status: 200, value: structuredClone(value) }, held);
  }

  private slaFindingKey(workspace: string, status: SLAStatus) { return `${workspace}:${status}`; }

  queueSLAFinding(workspace: string, status: SLAStatus, reply: Reply<SyntheticSLAFindingPage>, held = false) {
    if (!this.roles.has(workspace) || reply.status === 200 &&
      reply.value.items.some((item) => item.status !== status)) {
      throw new Error("Queued SLA finding page must contain only its requested status.");
    }
    return this.queue(this.slaFindingReplies, this.slaFindingKey(workspace, status),
      reply as Reply<unknown>, held);
  }

  queueSLAFindingRaw(workspace: string, status: SLAStatus, value: unknown, held = false) {
    if (!this.roles.has(workspace)) throw new Error("Raw SLA finding pages require a known synthetic workspace.");
    return this.queue(this.slaFindingReplies, this.slaFindingKey(workspace, status),
      { status: 200, value: structuredClone(value) }, held);
  }

  queueSLAPolicy(workspace: string, reply: Reply<SyntheticSLAPolicy>, held = false) {
    if (!this.roles.has(workspace) || reply.status === 200 && reply.value.workspaceId !== workspace) {
      throw new Error("Queued SLA policy must belong to its known synthetic workspace.");
    }
    const wrapped: Reply<unknown> = reply.status === 200
      ? { status: 200, value: syntheticSLAPolicyResponse(reply.value) }
      : reply;
    return this.queue(this.slaPolicyReplies, workspace, wrapped, held);
  }

  queueSLAPolicyRaw(workspace: string, value: unknown, held = false) {
    if (!this.roles.has(workspace)) throw new Error("Raw SLA policies require a known synthetic workspace.");
    return this.queue(this.slaPolicyReplies, workspace,
      { status: 200, value: structuredClone(value) }, held);
  }

  queueSLAPolicyUpdate(workspace: string, reply: Reply<SyntheticSLAPolicy>, held = false) {
    if (!this.roles.has(workspace) || reply.status === 200 && reply.value.workspaceId !== workspace) {
      throw new Error("Queued SLA policy acknowledgement must belong to its known synthetic workspace.");
    }
    const wrapped: Reply<unknown> = reply.status === 200
      ? { status: 200, value: syntheticSLAPolicyResponse(reply.value) }
      : reply;
    const control = this.queue(this.slaPolicyUpdateReplies, workspace, wrapped, held);
    if (reply.status === 200) {
      this.slaPolicyUpdateReplies.get(workspace)!.at(-1)!.commitPolicy = structuredClone(reply.value);
    }
    return control;
  }

  queueSLAPolicyUpdateRaw(workspace: string, value: unknown, held = false) {
    if (!this.roles.has(workspace)) throw new Error("Raw SLA policy acknowledgements require a known synthetic workspace.");
    return this.queue(this.slaPolicyUpdateReplies, workspace,
      { status: 200, value: structuredClone(value) }, held);
  }

  holdCreation(): ReportResponseControl {
    const gate = this.gate(true);
    this.createGates.push(gate);
    return gate;
  }

  setSnapshotState(id: string, state: SnapshotState) {
    const old = this.snapshots.get(id);
    if (!old || old.state === "succeeded" || old.state === "failed") throw new Error("Only nonterminal acknowledged synthetic jobs may advance.");
    const report = state === "succeeded" ? withFreshness({
      ...savedReport, workspaceId: old.workspaceId, asOf: "2026-09-14T06:08:09.000Z",
    }, old.freshnessDays) : null;
    this.snapshots.set(id, {
      ...old, state, report, completedAt: report?.asOf ?? null,
      failure: state === "failed" || state === "queued" ? {
        code: "report-generation-failed", message: "Synthetic snapshot generation could not be committed.", retryable: state === "queued",
      } : null,
    });
  }

  historyPage(workspace: string, limit = 100, cursor = ""): SnapshotPage {
    const all = [...this.snapshots.values()].filter((item) => item.workspaceId === workspace).sort((a, b) => a.id.localeCompare(b.id));
    const remaining = all.filter((item) => item.id > cursor);
    const items = remaining.slice(0, limit).map(snapshotSummary);
    return { apiVersion, dataOrigin: "synthetic", items, total: all.length, nextCursor: remaining.length > limit ? items.at(-1)!.id : null };
  }

  releaseResponses() { for (const gate of this.gates) gate.release(); }

  private async error(route: Route, status: number, message?: string) {
    const code = status === 401 ? "unauthorized" : status === 403 ? "forbidden" : status === 404 ? "not-found" :
      status === 400 ? "invalid-input" : status === 409 ? "conflict" :
        status === 413 ? "too-large" : status === 429 ? "rate-limited" : "unavailable";
    await route.fulfill({ status, json: { apiVersion, error: {
      code, message: message ?? (status === 401 ? "Synthetic reporting session ended." : status === 403 ? "Synthetic report access denied." :
        status === 404 ? "Synthetic snapshot not found." : status === 409 ? "Synthetic SLA policy revision conflict." :
          status === 413 ? "Synthetic historical trend window is too large." : "Synthetic report service unavailable."),
      requestId: "synthetic-reports-request", retryable: false,
    } } });
  }

  private async deliver<T>(route: Route, call: ReportCall, scheduled: Scheduled<T> | undefined, value: T, key: "report" | "snapshot" | null) {
    const reply = structuredClone(scheduled?.reply ?? { status: 200 as const, value });
    const gate = scheduled?.gate;
    try {
      if (gate) { gate.call = call; gate.arrive(); await gate.wait; }
      if (reply.status !== 200) {
        if (reply.status === 401) this.authenticated = false;
        await this.error(route, reply.status);
      } else {
        const json = key ? { apiVersion, dataOrigin: "synthetic", [key]: reply.value } : reply.value;
        if (key === null) this.pages.push({ call, response: structuredClone(reply.value as SnapshotPage) });
        await route.fulfill({ json });
      }
    } finally {
      gate?.complete();
      if (gate) this.gates.delete(gate);
    }
  }

  private async deliverEnvelope(route: Route, call: ReportCall, scheduled: Scheduled<unknown> | undefined, value: unknown) {
    const reply = structuredClone(scheduled?.reply ?? { status: 200 as const, value });
    const gate = scheduled?.gate;
    try {
      if (gate) { gate.call = call; gate.arrive(); await gate.wait; }
      if (reply.status !== 200) {
        if (reply.status === 401) this.authenticated = false;
        await this.error(route, reply.status);
      } else {
        await route.fulfill({ json: reply.value });
      }
    } finally {
      gate?.complete();
      if (gate) this.gates.delete(gate);
    }
  }

  async install(page: Page, origin: string) {
    if (new URL(origin).hostname !== "127.0.0.1") throw new Error("Reports fixtures only authorize the existing loopback HTTP test boundary.");
    await page.context().addCookies([{ name: "aspm_session", value: sessionCookie, url: origin, httpOnly: true, sameSite: "Lax" }]);
    page.on("requestfailed", (request) => {
      const call = this.callsByRequest.get(request);
      if (call) call.failure = request.failure()?.errorText ?? "request failed";
    });
    await page.route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) {
        this.violations.push("Unexpected external request; Reports tests authorize no native or third-party traffic.");
        await route.abort();
        return;
      }
      if (!url.pathname.startsWith("/api/")) { await route.continue(); return; }
      const headers = await request.allHeaders(), method = request.method(), path = url.pathname;
      if (headers.authorization) this.violations.push("Browser sent Authorization instead of its HttpOnly application session.");
      if (headers["x-aspm-bootstrap-token"]) this.violations.push("Reports must not send a bootstrap token.");
      if (secretCanaries.some((secret) => url.href.includes(secret) || url.href.includes(encodeURIComponent(secret)))) {
        this.violations.push("A synthetic credential was put in a request URL.");
      }
      const postWrites = [snapshotsPath, "/api/v1/login", "/api/v1/logout"];
      const allowedWrite = method === "POST" && postWrites.includes(path) ||
        method === "PATCH" && path === slaPolicyPath;
      if (method !== "GET" && !allowedWrite) {
        this.violations.push(`Undeclared Reports write: ${method} ${path}.`);
        await route.abort();
        return;
      }
      let body: Record<string, unknown> = {};
      if (method === "POST" || method === "PATCH") {
        if (headers.origin !== origin) this.violations.push("Application write omitted its matching Origin.");
        if (url.search !== "") this.violations.push("Reports writes must not move request fields into the URL.");
        if (path !== "/api/v1/logout") {
          if (!headers["content-type"]?.startsWith("application/json")) {
            this.violations.push("Reports writes require a JSON body.");
            await this.error(route, 400, "Synthetic Reports requires JSON.");
            return;
          }
          let parsed: unknown;
          try { parsed = request.postDataJSON(); } catch {
            this.violations.push("Malformed Reports JSON.");
            await this.error(route, 400, "Synthetic Reports requires valid JSON.");
            return;
          }
          if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
            this.violations.push("Reports writes require a JSON object.");
            await this.error(route, 400, "Synthetic Reports requires a JSON object.");
            return;
          }
          body = parsed as Record<string, unknown>;
        }
      }
      const call: ReportCall = {
        method, path, workspace: headers["x-aspm-workspace-id"], query: Object.fromEntries(url.searchParams),
        body, reportAsOf: headers["x-aspm-report-as-of"], failure: null,
      };
      this.requests.push(call);
      this.callsByRequest.set(request, call);
      if (this.requests.length > 60) {
        this.violations.push("Browser exceeded the original bounded 60-request per-case API budget.");
        await this.error(route, 429, "Synthetic request budget exceeded.");
        return;
      }
      const hasCookie = headers.cookie?.split(";").some((item) => item.trim() === `aspm_session=${sessionCookie}`);
      const session = () => ({
        apiVersion, user: reportUser, expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        workspaces: [reportAlpha, reportBeta].map((workspace) => ({ ...workspace, role: this.roles.get(workspace.id)! })),
      });
      if (method === "GET" && path === "/api/v1/session") {
        if (this.authenticated && hasCookie) {
          await route.fulfill({ json: session() });
          this.sessionCompleted = true;
        } else { await this.error(route, 401); }
        return;
      }
      if (method === "POST" && path === "/api/v1/login") {
        if (Object.keys(body).sort().join(",") !== "email,password" || body.email !== reportUser.email || body.password !== password) {
          await this.error(route, 401, "Synthetic sign-in rejected.");
        } else {
          this.authenticated = true;
          await route.fulfill({ json: session(), headers: { "set-cookie": `aspm_session=${sessionCookie}; Path=/; HttpOnly; SameSite=Lax` } });
          this.sessionCompleted = true;
        }
        return;
      }
      if (!this.sessionCompleted) this.violations.push("Protected Reports started before the authenticated session completed.");
      if (!this.authenticated || !hasCookie) { await this.error(route, 401); return; }
      if (method === "POST" && path === "/api/v1/logout") {
        this.authenticated = false;
        this.sessionCompleted = false;
        await route.fulfill({ status: 204, headers: { "set-cookie": "aspm_session=; Max-Age=0; Path=/; HttpOnly; SameSite=Lax" } });
        return;
      }
      const workspace = headers["x-aspm-workspace-id"];
      if (!workspace || !this.roles.has(workspace)) {
        this.violations.push("Protected Reports omitted or invented the session-owned selected workspace.");
        await this.error(route, 403);
        return;
      }
      try {
        const empty = emptySlackNavigation(url, method, workspace,
          new Map([reportAlpha.id, reportBeta.id].map((id) => [id, [findingResponse.finding.id]])));
        if (empty) { await route.fulfill({ json: empty }); return; }
      } catch (error) {
        this.violations.push(`Invalid additive Slack navigation: ${String(error)}`);
        await route.abort();
        return;
      }
      if (path === overviewPath && method === "GET") {
        let days: number;
        try { days = overviewDays(url); } catch (error) {
          this.violations.push(String(error));
          await this.error(route, 400, "Invalid synthetic overview query.");
          return;
        }
        const scheduled = this.overviewReplies.get(workspace)?.shift();
        if (scheduled?.reply.status === 200) scheduled.reply.value = withFreshness(scheduled.reply.value, days);
        await this.deliver(route, call, scheduled, withFreshness(this.overviews.get(workspace)!, days), "report");
      } else if (path === trendsPath && method === "GET") {
        let days: number;
        try { days = trendDays(url); } catch (error) {
          this.violations.push(String(error));
          await this.error(route, 400, "Invalid synthetic historical trend query.");
          return;
        }
        await this.deliverEnvelope(route, call, this.trendReplies.get(workspace)?.shift(),
          syntheticTrendResponse(workspace, days));
      } else if (path === coverageAssetsPath && method === "GET") {
        let parameters: ReturnType<typeof coverageParameters>;
        try { parameters = coverageParameters(url); } catch (error) {
          this.violations.push(String(error));
          await this.error(route, 400, "Invalid synthetic coverage drill-down query.");
          return;
        }
        const scheduled = this.coverageReplies.get(this.coverageKey(workspace, parameters.state))?.shift();
        await this.deliverEnvelope(route, call, scheduled,
          syntheticCoverageResponse(this.overviews.get(workspace)!, parameters.state,
            parameters.freshnessDays, parameters.limit, parameters.cursor));
      } else if (path === findingMetricsPath && method === "GET") {
        let parameters: ReturnType<typeof findingMetricParameters>;
        try { parameters = findingMetricParameters(url); } catch (error) {
          this.violations.push(String(error));
          await this.error(route, 400, "Invalid synthetic finding metric query.");
          return;
        }
        const overview = this.overviews.get(workspace)!;
        const reportAsOf = headers["x-aspm-report-as-of"];
        if (reportAsOf !== undefined && reportAsOf !== overview.asOf) {
          this.violations.push("Finding metric read did not bind to the displayed Live overview as-of.");
          await this.error(route, 400, "Invalid synthetic finding metric as-of.");
          return;
        }
        const scheduled = this.findingMetricReplies.get(
          this.findingMetricKey(workspace, parameters.metric))?.shift();
        await this.deliverEnvelope(route, call, scheduled,
          syntheticFindingMetricResponse(overview, parameters.metric,
            parameters.limit, parameters.cursor, reportAsOf ?? overview.asOf));
      } else if (path === slaPolicyPath && method === "GET") {
        try { noSLAQuery(url, "SLA policy"); } catch (error) {
          this.violations.push(String(error));
          await this.error(route, 400, "Invalid synthetic SLA policy query.");
          return;
        }
        await this.deliverEnvelope(route, call, this.slaPolicyReplies.get(workspace)?.shift(),
          syntheticSLAPolicyResponse(this.slaPolicies.get(workspace)!));
      } else if (path === slaPolicyPath && method === "PATCH") {
        try { noSLAQuery(url, "SLA policy update"); } catch (error) {
          this.violations.push(String(error));
          await this.error(route, 400, "Invalid synthetic SLA policy query.");
          return;
        }
        if (this.serverRoles.get(workspace) !== "admin") { await this.error(route, 403); return; }
        const keys = ["criticalDays", "highDays", "infoDays", "lowDays", "mediumDays", "rationale", "revision"];
        if (Object.keys(body).sort().join(",") !== keys.sort().join(",")) {
          await this.error(route, 400, "Synthetic SLA policy requires the exact complete shape.");
          return;
        }
        const current = this.slaPolicies.get(workspace)!;
        const targets = ["criticalDays", "highDays", "mediumDays", "lowDays", "infoDays"] as const;
        const values = targets.map((key) => body[key]);
        if (typeof body.revision !== "number" || !Number.isSafeInteger(body.revision) || body.revision < 1 ||
          values.some((value) => typeof value !== "number" || !Number.isInteger(value) || value < 1 || value > 3650) ||
          values.some((value, index) => index > 0 && Number(values[index - 1]) > Number(value)) ||
          typeof body.rationale !== "string" || body.rationale.trim() === "" ||
          body.rationale.includes("\0") || Buffer.byteLength(body.rationale, "utf8") > 8192) {
          await this.error(route, 400, "Invalid synthetic SLA policy input.");
          return;
        }
        if (body.revision !== current.revision) { await this.error(route, 409); return; }
        if (targets.every((key) => body[key] === current[key]) && body.rationale === current.rationale) {
          await this.error(route, 409);
          return;
        }
        const candidate: SyntheticSLAPolicy = {
          ...structuredClone(current),
          criticalDays: Number(body.criticalDays), highDays: Number(body.highDays),
          mediumDays: Number(body.mediumDays), lowDays: Number(body.lowDays), infoDays: Number(body.infoDays),
          revision: current.revision + 1, approvedBy: reportUser.id, approvedByName: reportUser.name,
          rationale: body.rationale, updatedAt: "2026-10-08T17:02:51.000Z",
        };
        const scheduled = this.slaPolicyUpdateReplies.get(workspace)?.shift();
        const reply = structuredClone(scheduled?.reply ??
          { status: 200 as const, value: syntheticSLAPolicyResponse(candidate) });
        const gate = scheduled?.gate;
        try {
          if (gate) { gate.call = call; gate.arrive(); await gate.wait; }
          if (reply.status !== 200) {
            if (reply.status === 401) this.authenticated = false;
            await this.error(route, reply.status);
          } else {
            const committed = scheduled?.commitPolicy ?? (!scheduled ? candidate : null);
            if (committed) this.slaPolicies.set(workspace, structuredClone(committed));
            await route.fulfill({ json: reply.value });
          }
        } finally {
          gate?.complete();
          if (gate) this.gates.delete(gate);
        }
      } else if (path === slaPath && method === "GET") {
        try { noSLAQuery(url, "SLA summary"); } catch (error) {
          this.violations.push(String(error));
          await this.error(route, 400, "Invalid synthetic SLA summary query.");
          return;
        }
        await this.deliverEnvelope(route, call, this.slaSummaryReplies.get(workspace)?.shift(),
          syntheticSLAResponse(workspace, this.slaPolicies.get(workspace)!));
      } else if (path === slaFindingsPath && method === "GET") {
        let parameters: ReturnType<typeof slaFindingParameters>;
        try { parameters = slaFindingParameters(url); } catch (error) {
          this.violations.push(String(error));
          await this.error(route, 400, "Invalid synthetic SLA finding query.");
          return;
        }
        const scheduled = this.slaFindingReplies.get(this.slaFindingKey(workspace, parameters.status))?.shift();
        await this.deliverEnvelope(route, call, scheduled,
          syntheticSLAFindingPage(workspace, parameters.status, parameters.limit, parameters.cursor,
            this.slaPolicies.get(workspace)!));
      } else if (path === snapshotsPath && method === "GET") {
        let parameters: ReturnType<typeof snapshotParameters>;
        try { parameters = snapshotParameters(url); } catch (error) {
          this.violations.push(String(error));
          await this.error(route, 400, "Invalid synthetic snapshot history query.");
          return;
        }
        const value = this.historyPage(workspace, parameters.limit, parameters.cursor);
        const held = this.historyHolds.get(workspace)?.shift();
        const scheduled = this.historyReplies.get(workspace)?.shift() ??
          (held ? { reply: { status: 200 as const, value }, gate: held } : undefined);
        await this.deliver(route, call, scheduled, value, null);
      } else if (path === snapshotsPath && method === "POST") {
        if (!["admin", "analyst"].includes(this.serverRoles.get(workspace)!)) { await this.error(route, 403); return; }
        const days = body.freshnessDays === undefined ? 7 : body.freshnessDays;
        if (Object.keys(body).some((key) => !["name", "freshnessDays"].includes(key)) ||
          typeof body.name !== "string" || body.name.trim() === "" || Buffer.byteLength(body.name, "utf8") > 256 || body.name.includes("\0") ||
          typeof days !== "number" || !Number.isInteger(days) || days < 1 || days > 365) {
          this.violations.push("Snapshot creation must use the real bounded name/freshnessDays JSON contract.");
          await this.error(route, 400, "Invalid synthetic snapshot input.");
          return;
        }
        const id = `e${(++this.createdCount).toString(16).padStart(31, "0")}`;
        const snapshot: SyntheticSnapshot = {
          id, workspaceId: workspace, name: body.name, requestedBy: reportUser.id, freshnessDays: days,
          state: "queued", createdAt: "2026-09-14T06:07:08.000Z", completedAt: null, failure: null, report: null,
        };
        this.snapshots.set(id, snapshot);
        const response = structuredClone(snapshot), gate = this.createGates.shift();
        try {
          if (gate) { gate.call = call; gate.arrive(); await gate.wait; }
          await route.fulfill({ status: 202, json: { apiVersion, dataOrigin: "synthetic", snapshot: response } });
        } finally {
          gate?.complete();
          if (gate) this.gates.delete(gate);
        }
      } else if (method === "GET" && path.startsWith(`${snapshotsPath}/`) && /^[a-f0-9]{32}$/.test(path.slice(snapshotsPath.length + 1))) {
        if (url.search !== "") this.violations.push("Selected snapshot reads do not support extra query filters.");
        const id = path.slice(snapshotsPath.length + 1), snapshot = this.snapshots.get(id);
        if (!snapshot || snapshot.workspaceId !== workspace) { await this.error(route, 404); return; }
        await this.deliver(route, call, this.snapshotReplies.get(id)?.shift(), snapshot, "snapshot");
      } else if (method === "GET" && path === "/api/v1/work" && url.search === "") {
        await route.fulfill({ json: workResponse() });
      } else if (method === "GET" && path === "/api/v1/assets" && url.search === "") {
        await route.fulfill({ json: { apiVersion, items: [], total: 0, nextCursor: null } });
      } else if (method === "GET" && path === "/api/v1/integrations/catalog" && url.search === "") {
        await route.fulfill({ json: catalogResponse });
      } else if (method === "GET" && path === `/api/v1/findings/${findingResponse.finding.id}` && url.search === "") {
        await route.fulfill({ json: findingResponse });
      } else {
        this.violations.push(`Undeclared Reports API operation: ${method} ${path}.`);
        await this.error(route, 404, "Undeclared synthetic Reports endpoint.");
      }
    });
  }
}

export const test = base.extend<{ reports: ReportsAPI }>({
  reports: async ({ page, baseURL }, use) => {
    if (!baseURL) throw new Error("Reports tests require the existing loopback browser baseURL.");
    const reports = new ReportsAPI(), pageErrors: string[] = [], consoleText: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    page.on("console", (message) => consoleText.push(message.text()));
    await reports.install(page, new URL(baseURL).origin);
    try { await use(reports); } finally {
      reports.releaseResponses();
      expect.soft(reports.violations, "Only declared same-origin Reports HTTP operations within the unchanged 60-call budget.").toEqual([]);
      expect.soft(pageErrors, "No unhandled errors from the real Reports UI.").toEqual([]);
      if (!page.isClosed() && page.url().startsWith(baseURL)) {
        const snapshot = await page.evaluate(() => ({
          visible: document.body.innerText, cookie: document.cookie,
          storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
        }));
        const storageText = JSON.stringify(snapshot.storage);
        expect.soft(snapshot.storage.map(([key]) => key).filter((key) => /token|api.?key|password|secret|credential/i.test(key))).toEqual([]);
        const reportCanaries = [
          alphaOverview.asOf, betaOverview.asOf, savedReport.asOf,
          ...savedSnapshots(reportAlpha.id, 2).flatMap((item) => [item.id, item.name]),
          ...trendStorageCanaries,
          ...coverageStorageCanaries,
          ...findingMetricStorageCanaries,
          ...slaStorageCanaries,
        ];
        for (const canary of reportCanaries) {
          expect.soft(storageText, "Report, snapshot, trend, coverage, finding and SLA data must not enter browser storage.").not.toContain(canary);
        }
        for (const secret of secretCanaries) {
          expect.soft(JSON.stringify(snapshot) + page.url() + consoleText.join("\n"),
            "Synthetic credentials must not leak into DOM, URL, readable cookies, storage or console.").not.toContain(secret);
        }
      }
    }
  },
});
export { expect } from "@playwright/test";
