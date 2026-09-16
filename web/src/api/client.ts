import { apiVersion } from "./types";
import type {
  Asset, AssetFields, AssetResponse, AssetsResponse, CatalogResponse, DataOrigin, FindingResponse,
  ImportInput, ImportReceipt, IntegrationSummary, JSONValue, Observation, Session, SourceScope, WorkItem, WorkResponse,
} from "./types";
import { rejectSession, requestAuthority } from "./authorization";

export class APIError extends Error {
  constructor(
    message: string,
    readonly code: "unauthorized" | "forbidden" | "not-found" | "unavailable" | "network" | "invalid-response" | "invalid-input" | "conflict" | "replay-expired" | "too-large" | "unsupported-format" | "method-not-allowed",
    readonly retryable: boolean,
    readonly requestId: string | null = null,
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

function parseAssets(value: unknown, workspace: string | null): AssetsResponse {
  const body = versioned(value);
  const items = uniqueIds(array(body.items, "asset list").map((value) => asset(value, workspace)));
  const total = count(body.total, "asset count");
  if (total < items.length) return invalid("asset count");
  return { apiVersion, items, total, nextCursor: nullableText(body.nextCursor, "asset cursor") };
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
}

export async function request<T>(path: string, parse: (value: unknown) => T, options: RequestOptions = {}): Promise<T> {
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
  if (response.status === 204) return parse(null);
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
    );
  }
  if (scoped && authority.revision !== requestAuthority().revision) throw new DOMException("Workspace changed", "AbortError");
  return parse(payload);
}

export const api = {
  work: (signal: AbortSignal) => request("/api/v1/work", parseWork, { signal }),
  finding: (id: string, signal: AbortSignal) => request(`/api/v1/findings/${encodeURIComponent(id)}`, parseFinding, { signal }),
  catalog: (signal: AbortSignal) => request("/api/v1/integrations/catalog", parseCatalog, { signal }),
  assets: (signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request("/api/v1/assets", (value) => parseAssets(value, workspace), { signal });
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
  session: (signal: AbortSignal) => request("/api/v1/session", parseSession, { signal, scoped: false }),
  login: (email: string, password: string, signal: AbortSignal) =>
    request("/api/v1/login", parseSession, { method: "POST", body: { email, password }, scoped: false, signal }),
  logout: () => request("/api/v1/logout", () => undefined, { method: "POST", scoped: false }),
  bootstrap: (body: { workspaceName: string; name: string; email: string; password: string }, token: string, signal: AbortSignal) =>
    request("/api/v1/bootstrap", (value) => object(value, "setup response"), {
      method: "POST", body, headers: { "X-ASPM-Bootstrap-Token": token }, scoped: false, signal,
    }),
};
