import { apiVersion } from "./types";
import type {
  Asset, AssetFields, AssetResponse, AssetsResponse, CatalogResponse, DataOrigin, FindingNoteResponse, FindingPatch, FindingResponse,
  ImportInput, ImportReceipt, IntegrationSummary, JSONValue, Observation, PostureReport, ReportOverviewResponse,
  ReportSnapshotInput, ReportSnapshotResponse, ReportSnapshotsResponse, ReportSnapshotSummary,
  Session, SourceScope, WorkItem, WorkResponse,
} from "./types";
import { rejectSession, requestAuthority } from "./authorization";

export class APIError extends Error {
  constructor(
    message: string,
    readonly code: "unauthorized" | "forbidden" | "not-found" | "unavailable" | "network" | "invalid-response" | "invalid-input" | "conflict" | "replay-expired" | "too-large" | "unsupported-format" | "method-not-allowed",
    readonly retryable: boolean,
    readonly requestId: string | null = null,
    readonly httpStatus: number | null = null,
  ) {
    super(message);
    this.name = "APIError";
  }
}

function invalid(field: string): never {
  throw new APIError(`The service returned an invalid ${field}. No replacement data was loaded.`, "invalid-response", true);
}

function object(value: unknown, field: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return invalid(field);
  return value as Record<string, unknown>;
}

function text(value: unknown, field: string, allowEmpty = false): string {
  if (typeof value !== "string" || (!allowEmpty && value.trim() === "")) return invalid(field);
  return value;
}

function choice<const T extends readonly string[]>(value: unknown, allowed: T, field: string): T[number] {
  if (typeof value !== "string" || !allowed.some((item) => item === value)) return invalid(field);
  return value;
}

function array(value: unknown, field: string): unknown[] {
  if (!Array.isArray(value)) return invalid(field);
  return value;
}

function boolean(value: unknown, field: string): boolean {
  if (typeof value !== "boolean") return invalid(field);
  return value;
}

function timestamp(value: unknown, field: string): string {
  const result = text(value, field);
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(result) || !Number.isFinite(Date.parse(result))) {
    return invalid(field);
  }
  return result;
}

function count(value: unknown, field: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) return invalid(field);
  return value;
}

function nullableText(value: unknown, field: string): string | null {
  return value === null ? null : text(value, field);
}

function nullableTimestamp(value: unknown, field: string): string | null {
  return value === null ? null : timestamp(value, field);
}

function versioned(value: unknown): Record<string, unknown> {
  const body = object(value, "response");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  return body;
}

function sourceScope(value: unknown): SourceScope {
  const scope = object(value, "source scope");
  return { id: text(scope.id, "scope identifier"), revision: text(scope.revision, "scope revision"), branch: text(scope.branch, "source branch") };
}

function jsonValue(value: unknown, depth = 0): JSONValue {
  if (depth > 64) return invalid("source context depth");
  if (value === null || typeof value === "string" || typeof value === "boolean") return value;
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (Array.isArray(value)) return value.map((item) => jsonValue(item, depth + 1));
  const fields = object(value, "source context");
  return Object.fromEntries(Object.entries(fields).map(([key, item]) => [key, jsonValue(item, depth + 1)]));
}

function observation(value: unknown): Observation {
  const item = object(value, "observation");
  const location = object(item.sourceLocation, "observation location");
  const unmapped = object(item.unmapped, "unmapped source fields");
  return {
    id: text(item.id, "observation identifier"), runId: text(item.runId, "run identifier"),
    sourceId: text(item.sourceId, "source identifier"), scanId: text(item.scanId, "scan identifier"),
    scope: sourceScope(item.scope), sourceScanAt: nullableTimestamp(item.sourceScanAt, "observation scan time"),
    sourceFindingId: text(item.sourceFindingId, "source finding identifier"),
    sourceSeverity: text(item.sourceSeverity, "source severity", true),
    normalizedSeverity: choice(item.normalizedSeverity, ["critical", "high", "medium", "low", "info"], "normalized severity"),
    sourceLocation: { uri: text(location.uri, "source location", true), line: count(location.line, "source line") },
    impact: text(item.impact, "source impact", true), remediation: text(item.remediation, "observation remediation", true),
    unmapped: Object.fromEntries(Object.entries(unmapped).map(([key, value]) => [key, jsonValue(value)])),
    evidenceDigest: text(item.evidenceDigest, "evidence digest"),
  };
}

function envelope(value: unknown): { body: Record<string, unknown>; dataOrigin: DataOrigin } {
  const body = object(value, "response");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  return { body, dataOrigin: choice(body.dataOrigin, ["synthetic", "live"], "data origin") };
}

function workItem(value: unknown): WorkItem {
  const item = object(value, "finding");
  return {
    id: text(item.id, "finding identifier"),
    title: text(item.title, "finding title"),
    assetName: text(item.assetName, "asset name"),
    severity: choice(item.severity, ["critical", "high", "medium", "low", "info"], "severity"),
    ownerName: item.ownerName === null ? null : text(item.ownerName, "owner name"),
    workflowState: choice(item.workflowState, ["open", "in-progress", "resolved"], "workflow state"),
    sourceScanAt: item.sourceScanAt === null ? null : timestamp(item.sourceScanAt, "source scan timestamp"),
    collectedAt: timestamp(item.collectedAt, "collection timestamp"),
    importedAt: timestamp(item.importedAt, "import timestamp"),
  };
}

function uniqueIds<T extends { id: string }>(items: T[]): T[] {
  if (new Set(items.map((item) => item.id)).size !== items.length) return invalid("duplicate record identifier");
  return items;
}

export function parseWork(value: unknown): WorkResponse {
  const { body, dataOrigin } = envelope(value);
  const items = uniqueIds(array(body.items, "finding list").map(workItem));
  if (typeof body.total !== "number" || !Number.isSafeInteger(body.total) || body.total < items.length) return invalid("finding count");
  return {
    apiVersion, dataOrigin, items, total: body.total,
    nextCursor: body.nextCursor === null ? null : text(body.nextCursor, "pagination cursor"),
  };
}

export function parseFinding(value: unknown): FindingResponse {
  const { body, dataOrigin } = envelope(value);
  const finding = object(body.finding, "finding detail");
  const evidence = object(finding.evidence, "evidence");
  return {
    apiVersion, dataOrigin,
    finding: {
      ...workItem(finding),
      scopeLabel: text(finding.scopeLabel, "finding scope"),
      description: text(finding.description, "description", true),
      evidence: {
        text: text(evidence.text, "evidence text", true),
        sourceLabel: text(evidence.sourceLabel, "evidence source"),
        verificationState: choice(evidence.verificationState, ["not-run", "blocked", "verified"], "verification state"),
      },
      remediation: text(finding.remediation, "remediation", true),
      assetId: finding.assetId === undefined ? undefined : text(finding.assetId, "asset identifier"),
      workspaceId: finding.workspaceId === undefined ? undefined : text(finding.workspaceId, "workspace identifier"),
      ownerId: finding.ownerId === undefined ? undefined : nullableText(finding.ownerId, "owner identifier"),
      sourceState: finding.sourceState === undefined ? undefined : choice(finding.sourceState, ["observed", "unknown", "stale", "inferred-resolved"], "source state"),
      sourceFreshnessAt: finding.sourceFreshnessAt === undefined ? undefined : nullableTimestamp(finding.sourceFreshnessAt, "source freshness"),
      disposition: finding.disposition === undefined ? undefined : choice(finding.disposition, ["none", "accepted-risk"], "human disposition"),
      acceptedRiskExpiresAt: finding.acceptedRiskExpiresAt === undefined ? undefined : nullableTimestamp(finding.acceptedRiskExpiresAt, "risk acceptance expiry"),
      riskAcceptanceExpired: finding.riskAcceptanceExpired === undefined ? undefined : boolean(finding.riskAcceptanceExpired, "risk acceptance expiry state"),
      verifiedResolution: finding.verifiedResolution === undefined ? undefined : boolean(finding.verifiedResolution, "resolution verification"),
      notes: finding.notes === undefined ? undefined : uniqueIds(array(finding.notes, "analyst notes").map((value) => {
        const note = object(value, "analyst note");
        return { id: text(note.id, "note identifier"), text: text(note.text, "note text", true) };
      })),
      observations: finding.observations === undefined ? undefined : uniqueIds(array(finding.observations, "observations").map(observation)),
      notesNextCursor: finding.notesNextCursor === undefined ? undefined : nullableText(finding.notesNextCursor, "notes cursor"),
      observationsNextCursor: finding.observationsNextCursor === undefined ? undefined : nullableText(finding.observationsNextCursor, "observations cursor"),
    },
  };
}

function parseFindingUpdate(value: unknown, id: string, workspace: string | null): FindingResponse {
  const result = parseFinding(value);
  const finding = result.finding;
  if (finding.id !== id || finding.workspaceId !== workspace ||
    finding.ownerId === undefined || (finding.ownerId !== null && !/^[a-f0-9]{32}$/.test(finding.ownerId)) ||
    (finding.ownerId === null) !== (finding.ownerName === null) ||
    finding.disposition === undefined || finding.acceptedRiskExpiresAt === undefined ||
    finding.riskAcceptanceExpired === undefined || finding.verifiedResolution === undefined ||
    finding.sourceState === undefined || finding.sourceFreshnessAt === undefined ||
    finding.notes === undefined || finding.observations === undefined) return invalid("updated finding");
  return result;
}

function parseFindingNote(value: unknown, submittedText: string): FindingNoteResponse {
  const item = object(versioned(value).note, "note receipt");
  const id = text(item.id, "note identifier");
  const noteText = text(item.text, "note text");
  if (!/^[a-f0-9]{32}$/.test(id) || noteText !== submittedText) return invalid("note acknowledgement");
  return { apiVersion, note: { id, text: noteText } };
}

function findingCursor(value: string | undefined): string {
  if (value === undefined) return "";
  if (typeof value !== "string" || (value !== "" && !/^[a-f0-9]{32}$/.test(value))) {
    throw new APIError("Finding history requires a native 32-character lowercase hexadecimal cursor.", "invalid-input", false);
  }
  return value;
}

function validateFindingHistoryPage(items: readonly { id: string }[] | undefined, next: string | null | undefined, cursor: string, field: string) {
  if ((items !== undefined && items.length > 500) ||
    (cursor !== "" && (items === undefined || next === undefined)) ||
    (next != null && (!/^[a-f0-9]{32}$/.test(next) || next !== items?.at(-1)?.id))) return invalid(`${field} history page`);
  if ((cursor !== "" || next != null) && items?.some((item, index) =>
    !/^[a-f0-9]{32}$/.test(item.id) || item.id <= (index === 0 ? cursor : items[index - 1].id))) return invalid(`${field} history order`);
}

function findingBodyLimit(body: FindingPatch | { text: string }, bytes: number): void {
  if (new TextEncoder().encode(JSON.stringify(body)).byteLength > bytes) {
    throw new APIError("The encoded finding action exceeds the service's request size limit. Shorten the input and retry.", "invalid-input", false);
  }
}

function asset(value: unknown, workspace: string | null): Asset {
  const item = object(value, "asset");
  const workspaceId = text(item.workspaceId, "asset workspace");
  if (workspace !== workspaceId) return invalid("asset workspace");
  return {
    id: text(item.id, "asset identifier"), workspaceId, name: text(item.name, "asset name"),
    kind: text(item.kind, "asset kind"), environment: text(item.environment, "asset environment", true),
    criticality: choice(item.criticality, ["low", "medium", "high", "critical"], "asset criticality"),
    tags: array(item.tags, "asset tags").map((value) => text(value, "asset tag")),
    ownerId: nullableText(item.ownerId, "asset owner"),
  };
}

function parseAssets(value: unknown, workspace: string | null, limit: number, cursor: string): AssetsResponse {
  const body = versioned(value);
  const items = uniqueIds(array(body.items, "asset list").map((value) => asset(value, workspace)));
  const total = count(body.total, "asset count");
  const nextCursor = nullableText(body.nextCursor, "asset cursor");
  if (total < items.length || items.length > limit) return invalid("asset count");
  if (nextCursor !== null && (!/^[a-f0-9]{32}$/.test(nextCursor) || nextCursor !== items.at(-1)?.id)) return invalid("asset cursor");
  if ((cursor !== "" || nextCursor !== null) && items.some((item, index) =>
    !/^[a-f0-9]{32}$/.test(item.id) || item.id <= (index === 0 ? cursor : items[index - 1].id))) return invalid("asset page order");
  return { apiVersion, items, total, nextCursor };
}

function parseAsset(value: unknown, workspace: string | null): AssetResponse {
  return { apiVersion, asset: asset(versioned(value).asset, workspace) };
}

function parseImport(value: unknown): ImportReceipt {
  const item = object(versioned(value).import, "import receipt");
  const state = choice(item.state, ["queued", "processing", "succeeded", "failed"], "import state");
  let failure: ImportReceipt["failure"] = null;
  if (item.failure !== null && item.failure !== undefined) {
    const detail = object(item.failure, "import failure");
    failure = { code: text(detail.code, "import failure code"), message: text(detail.message, "import failure message") };
  }
  if (state === "failed" && failure === null) return invalid("import failure");
  return {
    id: text(item.id, "import identifier"), runId: text(item.runId, "import run identifier"), state,
    assetId: text(item.assetId, "import asset"), format: text(item.format, "report format"),
    sourceId: text(item.sourceId, "import source"), scanId: text(item.scanId, "import scan"),
    scope: sourceScope(item.scope), sourceScanAt: nullableTimestamp(item.sourceScanAt, "source scan time"),
    collectedAt: timestamp(item.collectedAt, "collection time"), importedAt: timestamp(item.importedAt, "acceptance time"),
    reportDigest: text(item.reportDigest, "report digest"), observationCount: count(item.observationCount, "observation count"), failure,
  };
}

function reportIdentifier(value: unknown, field: string): string {
  const id = text(value, field);
  if (!/^[a-f0-9]{32}$/.test(id)) return invalid(field);
  return id;
}

function reportFreshness(value: unknown): number {
  const days = count(value, "report freshness days");
  if (days < 1 || days > 365) return invalid("report freshness days");
  return days;
}

function postureReport(value: unknown, workspace: string | null, days: number): PostureReport {
  const item = object(value, "posture report");
  const workspaceId = text(item.workspaceId, "report workspace");
  if (workspaceId !== workspace) return invalid("report workspace");
  const totals = object(item.totals, "posture totals");
  const severity = object(item.bySeverity, "severity counts");
  const coverage = object(item.coverage, "posture coverage");
  const window = object(item.freshnessWindow, "freshness window");
  const verification = object(item.verification, "report verification");
  const asOf = timestamp(item.asOf, "report as-of time");
  const freshnessWindow = {
    from: timestamp(window.from, "freshness start"), to: timestamp(window.to, "freshness end"),
    days: reportFreshness(window.days),
  };
  const freshnessWindowDays = reportFreshness(coverage.freshnessWindowDays);
  if (freshnessWindow.days !== days || freshnessWindowDays !== days ||
    Date.parse(freshnessWindow.to) !== Date.parse(asOf) ||
    Date.parse(freshnessWindow.from) >= Date.parse(freshnessWindow.to)) return invalid("report freshness window");
  return {
    workspaceId, asOf, freshnessWindow,
    totals: {
      assets: count(totals.assets, "asset total"), findings: count(totals.findings, "finding total"),
      openFindings: count(totals.openFindings, "open finding total"),
      acceptedRisk: count(totals.acceptedRisk, "accepted risk total"),
      expiredAcceptedRisk: count(totals.expiredAcceptedRisk, "expired accepted risk total"),
      inferredResolved: count(totals.inferredResolved, "source-inferred resolution total"),
      verifiedResolved: count(totals.verifiedResolved, "verified resolution total"),
    },
    bySeverity: {
      critical: count(severity.critical, "critical findings"), high: count(severity.high, "high findings"),
      medium: count(severity.medium, "medium findings"), low: count(severity.low, "low findings"),
      info: count(severity.info, "informational findings"),
    },
    coverage: {
      scannedAssets: count(coverage.scannedAssets, "scanned assets"),
      unscannedAssets: count(coverage.unscannedAssets, "unscanned assets"),
      staleAssets: count(coverage.staleAssets, "stale assets"),
      unknownFreshnessAssets: count(coverage.unknownFreshnessAssets, "unknown freshness assets"),
      freshnessWindowDays,
    },
    verification: {
      state: choice(verification.state, ["not-run"], "report verification state"),
      reason: text(verification.reason, "report verification limitation"),
    },
  };
}

function parseReportOverview(value: unknown, workspace: string | null, days: number): ReportOverviewResponse {
  const { body, dataOrigin } = envelope(value);
  return { apiVersion, dataOrigin, report: postureReport(body.report, workspace, days) };
}

function reportSnapshotSummary(value: unknown, workspace: string | null): ReportSnapshotSummary {
  const item = object(value, "snapshot summary");
  const workspaceId = text(item.workspaceId, "snapshot workspace");
  if (workspaceId !== workspace) return invalid("snapshot workspace");
  const name = text(item.name, "snapshot name");
  if (name.includes("\0") || new TextEncoder().encode(name).byteLength > 256) return invalid("snapshot name");
  const state = choice(item.state, ["queued", "processing", "succeeded", "failed"], "snapshot state");
  const completedAt = nullableTimestamp(item.completedAt, "snapshot completion time");
  let failure: ReportSnapshotSummary["failure"] = null;
  if (item.failure !== null) {
    const detail = object(item.failure, "snapshot failure");
    failure = {
      code: text(detail.code, "snapshot failure code"), message: text(detail.message, "snapshot failure message"),
      retryable: boolean(detail.retryable, "snapshot retry policy"),
    };
  }
  if ((state === "succeeded") !== (completedAt !== null) ||
    (state === "succeeded" && failure !== null) || (state === "failed" && failure === null) ||
    (failure !== null && failure.retryable !== (state === "queued"))) return invalid("snapshot worker state");
  return {
    id: reportIdentifier(item.id, "snapshot identifier"), workspaceId, name,
    requestedBy: reportIdentifier(item.requestedBy, "snapshot requester"), state, completedAt, failure,
    freshnessDays: reportFreshness(item.freshnessDays), createdAt: timestamp(item.createdAt, "snapshot creation time"),
  };
}

function parseReportSnapshot(value: unknown, workspace: string | null): ReportSnapshotResponse {
  const { body, dataOrigin } = envelope(value);
  const item = object(body.snapshot, "snapshot detail");
  const summary = reportSnapshotSummary(item, workspace);
  const report = item.report === null ? null : postureReport(item.report, workspace, summary.freshnessDays);
  if ((summary.state === "succeeded") !== (report !== null) ||
    (report !== null && Date.parse(report.asOf) !== Date.parse(summary.completedAt!))) return invalid("saved snapshot report");
  return { apiVersion, dataOrigin, snapshot: { ...summary, report } };
}

function parseReportSnapshots(value: unknown, workspace: string | null, limit: number, cursor: string | null): ReportSnapshotsResponse {
  const { body, dataOrigin } = envelope(value);
  const items = uniqueIds(array(body.items, "snapshot history").map((value) => reportSnapshotSummary(value, workspace)));
  const total = count(body.total, "snapshot count");
  const nextCursor = body.nextCursor === null ? null : reportIdentifier(body.nextCursor, "snapshot cursor");
  if (total < items.length || items.length > limit ||
    items.some((item, index) => item.id <= (index === 0 ? cursor ?? "" : items[index - 1].id)) ||
    (nextCursor !== null && nextCursor !== items.at(-1)?.id)) return invalid("snapshot history page");
  return { apiVersion, dataOrigin, items, total, nextCursor };
}

export function parseCatalog(value: unknown): CatalogResponse {
  const { body, dataOrigin } = envelope(value);
  const items = array(body.items, "integration catalog").map((value): IntegrationSummary => {
    const item = object(value, "integration");
    const verification = object(item.liveVerification, "integration verification");
    return {
      id: choice(item.id, ["github", "gitlab", "azure-devops", "aws", "azure", "jira", "teams", "slack"], "integration family"),
      name: text(item.name, "integration name"),
      kind: choice(item.kind, ["native"], "integration kind"),
      capabilities: array(item.capabilities, "capabilities").map((item) => text(item, "capability")),
      supportMaturity: choice(item.supportMaturity, ["planned", "experimental", "supported"], "support maturity"),
      connectionState: choice(item.connectionState, ["unconfigured", "healthy", "stale", "failed", "partially-authorized"], "connection state"),
      readyToConnect: boolean(item.readyToConnect, "connection readiness"),
      liveVerification: {
        state: choice(verification.state, ["not-run", "blocked", "passed", "failed"], "live verification state"),
        reason: text(verification.reason, "verification reason"),
      },
    };
  });
  return { apiVersion, dataOrigin, items: uniqueIds(items) };
}

export function parseSession(value: unknown): Session {
  const body = object(value, "session");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  const user = object(body.user, "session user");
  const workspaces = uniqueIds(array(body.workspaces, "memberships").map((value) => {
    const member = object(value, "workspace membership");
    return {
      id: text(member.id, "workspace identifier"), name: text(member.name, "workspace name"),
      role: choice(member.role, ["admin", "analyst", "viewer"], "workspace role"),
    };
  }));
  return {
    apiVersion, user: { id: text(user.id, "user identifier"), name: text(user.name, "user name"), email: text(user.email, "email") },
    workspaces, expiresAt: timestamp(body.expiresAt, "session expiry"),
  };
}

interface RequestOptions {
  signal?: AbortSignal;
  method?: "GET" | "POST" | "PATCH" | "DELETE";
  body?: unknown;
  scoped?: boolean;
  headers?: Record<string, string>;
  expectedStatus?: 200 | 201 | 202 | readonly (200 | 201 | 202)[];
  decodeBody?: (response: Response) => Promise<unknown>;
}

export async function request<T>(path: string, parse: (value: unknown, status: number) => T, options: RequestOptions = {}): Promise<T> {
  const authority = requestAuthority();
  const scoped = options.scoped !== false;
  if (scoped && !authority.workspace) throw new APIError("Sign in to continue.", "unauthorized", false);
  const signal = options.signal ?? new AbortController().signal;
  const signals = [signal, AbortSignal.timeout(15_000)];
  if (scoped) signals.push(authority.signal);
  let response: Response;
  try {
    response = await fetch(path, {
      method: options.method ?? "GET", headers: {
        Accept: "application/json", ...(options.body === undefined ? {} : { "Content-Type": "application/json" }),
        ...(scoped ? { "X-ASPM-Workspace-ID": authority.workspace! } : {}), ...options.headers,
      },
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: "same-origin", mode: "same-origin", redirect: "error", cache: "no-store",
      signal: AbortSignal.any(signals),
    });
  } catch (error) {
    if (signal.aborted || (scoped && authority.signal.aborted)) throw error;
    throw new APIError("Unable to reach the data service. Check the service connection and retry.", "network", true);
  }
  if (scoped && authority.revision !== requestAuthority().revision) throw new DOMException("Workspace changed", "AbortError");
  if (response.status === 401 && scoped) rejectSession(authority.revision);
  if (response.ok && options.expectedStatus !== undefined) {
    const accepted = typeof options.expectedStatus === "number" ? [options.expectedStatus] : options.expectedStatus;
    if (!accepted.some((status) => status === response.status)) return invalid("HTTP response status");
  }
  if (response.status === 204) return parse(null, response.status);
  if (response.ok && options.decodeBody) {
    const payload = await options.decodeBody(response);
    if (signal.aborted || (scoped && authority.revision !== requestAuthority().revision)) {
      throw new DOMException("Request scope ended", "AbortError");
    }
    return parse(payload, response.status);
  }
  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new APIError("The data service did not return JSON. Check that the API is running, then retry.", "invalid-response", true);
  }
  if (!response.ok) {
    const body = object(payload, "error response");
    if (body.apiVersion !== apiVersion) return invalid("error API version");
    const error = object(body.error, "error detail");
    throw new APIError(
      text(error.message, "error message"),
      choice(error.code, ["unauthorized", "forbidden", "not-found", "unavailable", "invalid-input", "conflict", "replay-expired", "too-large", "unsupported-format", "method-not-allowed"], "error code"),
      boolean(error.retryable, "retry policy"),
      text(error.requestId, "request identifier"),
      response.status,
    );
  }
  if (scoped && authority.revision !== requestAuthority().revision) throw new DOMException("Workspace changed", "AbortError");
  return parse(payload, response.status);
}

async function reportRead<T>(path: string, parse: (value: unknown) => T, signal: AbortSignal): Promise<T> {
  const scopedSignal = AbortSignal.any([signal, requestAuthority().signal]);
  // Allow an abandoned mounting effect to cancel before starting network I/O.
  await Promise.resolve();
  scopedSignal.throwIfAborted();
  return request(path, parse, { signal: scopedSignal, expectedStatus: 200 });
}

export const api = {
  work: (signal: AbortSignal) => request("/api/v1/work", parseWork, { signal }),
  finding: (id: string, signal: AbortSignal, cursors?: { notesCursor?: string; observationsCursor?: string }) => {
    const workspace = requestAuthority().workspace;
    const notesCursor = findingCursor(cursors?.notesCursor), observationsCursor = findingCursor(cursors?.observationsCursor);
    const query = new URLSearchParams();
    if (notesCursor !== "") query.set("notesCursor", notesCursor);
    if (observationsCursor !== "") query.set("observationsCursor", observationsCursor);
    const path = `/api/v1/findings/${encodeURIComponent(id)}`;
    return request(query.size === 0 ? path : `${path}?${query}`, (value) => {
      const result = parseFinding(value);
      if (result.finding.id !== id || (result.finding.workspaceId !== undefined && result.finding.workspaceId !== workspace)) {
        return invalid("selected finding");
      }
      validateFindingHistoryPage(result.finding.notes, result.finding.notesNextCursor, notesCursor, "notes");
      validateFindingHistoryPage(result.finding.observations, result.finding.observationsNextCursor, observationsCursor, "observations");
      return result;
    }, { signal, expectedStatus: 200 });
  },
  updateFinding: (id: string, input: FindingPatch, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    const body: FindingPatch = {};
    if (input.ownerId !== undefined) body.ownerId = input.ownerId;
    if (input.workflowState !== undefined) body.workflowState = input.workflowState;
    if (input.disposition !== undefined) body.disposition = input.disposition;
    if (input.acceptedRiskExpiresAt !== undefined) body.acceptedRiskExpiresAt = input.acceptedRiskExpiresAt;
    findingBodyLimit(body, 16 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(id)}`, (value) => parseFindingUpdate(value, id, workspace),
      { method: "PATCH", body, signal, expectedStatus: 200 });
  },
  addFindingNote: (id: string, noteText: string, signal: AbortSignal) => {
    if (noteText.trim() === "" || noteText.includes("\0") || new TextEncoder().encode(noteText).byteLength > 8192) {
      throw new APIError("A note must be nonblank, NUL-free and at most 8192 UTF-8 bytes. Your draft has not been changed.", "invalid-input", false);
    }
    const body = { text: noteText };
    findingBodyLimit(body, 32 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(id)}/notes`, (value) => parseFindingNote(value, noteText),
      { method: "POST", body, signal, expectedStatus: 201 });
  },
  catalog: (signal: AbortSignal) => request("/api/v1/integrations/catalog", parseCatalog, { signal }),
  assets: (signal: AbortSignal, options?: { cursor?: string; limit?: number }) => {
    const workspace = requestAuthority().workspace;
    const cursor = options?.cursor === undefined ? "" : options.cursor;
    const limit = options?.limit === undefined ? 100 : options.limit;
    if (!Number.isInteger(limit) || limit < 1 || limit > 500 ||
      typeof cursor !== "string" || (cursor !== "" && !/^[a-f0-9]{32}$/.test(cursor))) {
      throw new APIError("Asset pages require a limit from 1 to 500 and a valid native cursor.", "invalid-input", false);
    }
    const query = new URLSearchParams();
    if (cursor !== "") query.set("cursor", cursor);
    if (options?.limit !== undefined) query.set("limit", String(limit));
    const path = query.size === 0 ? "/api/v1/assets" : `/api/v1/assets?${query}`;
    return request(path, (value) => parseAssets(value, workspace, limit, cursor), { signal, expectedStatus: 200 });
  },
  createAsset: (body: AssetFields, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request("/api/v1/assets", (value) => parseAsset(value, workspace), { method: "POST", body, signal });
  },
  updateAsset: (id: string, body: Partial<AssetFields>, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request(`/api/v1/assets/${encodeURIComponent(id)}`, (value) => {
      const result = parseAsset(value, workspace);
      if (result.asset.id !== id) return invalid("updated asset identifier");
      return result;
    }, { method: "PATCH", body, signal });
  },
  importReport: (body: ImportInput, signal: AbortSignal) => request("/api/v1/imports", parseImport, { method: "POST", body, signal }),
  importStatus: (id: string, signal: AbortSignal) => request(`/api/v1/imports/${encodeURIComponent(id)}`, (value) => {
    const result = parseImport(value);
    if (result.id !== id) return invalid("import identifier");
    return result;
  }, { signal }),
  reportOverview: (days: number, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return reportRead(`/api/v1/reports/overview?freshnessDays=${days}`,
      (value) => parseReportOverview(value, workspace, days), signal);
  },
  reportSnapshots: (limit: number, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    const query = new URLSearchParams({ limit: String(limit) });
    if (cursor !== null) query.set("cursor", cursor);
    return reportRead(`/api/v1/reports/snapshots?${query}`,
      (value) => parseReportSnapshots(value, workspace, limit, cursor), signal);
  },
  reportSnapshot: (id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return reportRead(`/api/v1/reports/snapshots/${encodeURIComponent(id)}`, (value) => {
      const result = parseReportSnapshot(value, workspace);
      if (result.snapshot.id !== id) return invalid("selected snapshot identifier");
      return result;
    }, signal);
  },
  createReportSnapshot: (input: ReportSnapshotInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    const body = { name: input.name, freshnessDays: input.freshnessDays };
    return request("/api/v1/reports/snapshots", (value) => {
      const result = parseReportSnapshot(value, workspace);
      if (result.snapshot.state !== "queued" || result.snapshot.name !== body.name ||
        result.snapshot.freshnessDays !== body.freshnessDays) return invalid("queued snapshot acknowledgement");
      return result;
    }, { method: "POST", body, signal, expectedStatus: 202 });
  },
  session: (signal: AbortSignal) => request("/api/v1/session", parseSession, { signal, scoped: false }),
  login: (email: string, password: string, signal: AbortSignal) =>
    request("/api/v1/login", parseSession, { method: "POST", body: { email, password }, scoped: false, signal }),
  logout: () => request("/api/v1/logout", () => undefined, { method: "POST", scoped: false }),
  bootstrap: (body: { workspaceName: string; name: string; email: string; password: string }, token: string, signal: AbortSignal) =>
    request("/api/v1/bootstrap", (value) => object(value, "setup response"), {
      method: "POST", body, headers: { "X-ASPM-Bootstrap-Token": token }, scoped: false, signal,
    }),
};
