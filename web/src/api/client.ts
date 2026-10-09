import { apiVersion } from "./types";
import type {
  ArchivedHistory, Asset, AssetFields, AssetResponse, AssetsResponse, CatalogResponse, DataOrigin, FindingBulkPatch, FindingBulkResponse, FindingCorrelation,
  CoverageAssetDrilldownResponse, CoverageAssetItem, CoverageAssetState,
  FindingMetric, FindingMetricItem, FindingMetricResponse,
  FindingCorrelationCandidatesResponse, FindingCorrelationMember, FindingCorrelationResponse,
  FindingDecision, FindingDecisionEvent, FindingDispositionApproval, FindingMergeInput, FindingMergePreviewResponse,
  FindingNoteResponse, FindingPatch, FindingResponse, FindingSplitInput, FindingSplitPreviewResponse,
  HistoricalTrendDelta, HistoricalTrendPoint, HistoricalTrendResponse,
  ImportInput, ImportReceipt, IntegrationSummary, JSONValue, Observation, PostureReport, ReportIntakeSummary, ReportOverviewResponse,
  RemediationSLAFinding, RemediationSLAFindingPage, RemediationSLAResponse, RemediationSLAStatus,
  ReportSLAPolicy, ReportSLAPolicyInput, ReportSLAPolicyResponse,
  ReportExport, ReportExportContent, ReportExportInput, ReportExportResponse, ReportExportsResponse,
  ReportSnapshotInput, ReportSnapshotResponse, ReportSnapshotsResponse, ReportSnapshotSummary,
  DeterministicVerification, DeterministicVerificationApproval, DeterministicVerificationEvidence,
  DeterministicVerificationPage, DeterministicVerificationResponse,
  VerificationApprovalPage, VerificationApprovalResponse, VerificationEvidencePage, VerificationEvidenceResponse,
  RetentionClassSummary, RetentionHold, RetentionHoldResponse, RetentionHoldsResponse, RetentionPolicyInput,
  RetentionPolicyResponse, RetentionPreview, RetentionPreviewItem, RetentionPreviewResponse,
  HistoryRetentionResourceKind, RetentionRun, RetentionRunItem, RetentionRunResponse,
  Session, SourceScope, WorkItem, WorkResponse,
} from "./types";
import { rejectSession, requestAuthority } from "./authorization";

export class APIError extends Error {
  constructor(
    message: string,
    readonly code: "unauthorized" | "forbidden" | "not-found" | "unavailable" | "network" |
      "invalid-response" | "invalid-input" | "conflict" | "replay-expired" | "too-large" |
      "unsupported-format" | "method-not-allowed" | "evidence-expired" | "evidence-missing" |
      "evidence-corrupt" | "history-not-archived",
    readonly retryable: boolean,
    readonly requestId: string | null = null,
    readonly httpStatus: number | null = null,
  ) {
    super(message);
    this.name = "APIError";
  }
}

function invalid(field: string, httpStatus: number | null = null): never {
  throw new APIError(`The service returned an invalid ${field}. No replacement data was loaded.`, "invalid-response", true, null, httpStatus);
}

function object(value: unknown, field: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return invalid(field);
  return value as Record<string, unknown>;
}

function exactObject(value: unknown, field: string, keys: readonly string[]): Record<string, unknown> {
  const result = object(value, field);
  const actual = Object.keys(result).sort();
  const expected = [...keys].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) return invalid(field);
  return result;
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

function signedInteger(value: unknown, field: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value)) return invalid(field);
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

function findingDecision(value: unknown): FindingDecision {
  const item = object(value, "finding decision");
  return {
    ownerId: nullableText(item.ownerId, "decision owner"),
    workflowState: choice(item.workflowState, ["open", "in-progress", "pending-retest", "resolved"], "decision workflow"),
    disposition: choice(item.disposition, ["none", "accepted-risk", "suppressed", "false-positive"], "decision disposition"),
    acceptedRiskExpiresAt: nullableTimestamp(item.acceptedRiskExpiresAt, "decision expiry"),
    dispositionScope: choice(item.dispositionScope, ["", "finding", "asset", "source", "scope"], "decision scope"),
    suppressionExpiresAt: nullableTimestamp(item.suppressionExpiresAt, "suppression expiry"),
    dispositionRationale: text(item.dispositionRationale, "disposition rationale", true),
  };
}

function findingDispositionApproval(value: unknown): FindingDispositionApproval {
  const item = object(value, "finding disposition approval");
  const disposition = choice(item.disposition,
    ["accepted-risk", "suppressed", "false-positive"], "approved disposition");
  const expiresAt = nullableTimestamp(item.expiresAt, "approval expiry");
  if (disposition === "suppressed" && expiresAt === null ||
    disposition === "false-positive" && expiresAt !== null) return invalid("disposition approval semantics");
  return {
    id: reportIdentifier(item.id, "disposition approval"),
    findingId: reportIdentifier(item.findingId, "approved finding"),
    decisionRevision: count(item.decisionRevision, "approval decision revision"),
    actorId: reportIdentifier(item.actorId, "approval actor"),
    actorName: text(item.actorName, "approval actor name"),
    disposition,
    scopeKind: choice(item.scopeKind, ["finding", "asset", "source", "scope"], "approval scope"),
    scopeValue: text(item.scopeValue, "approval scope value"),
    rationale: text(item.rationale, "approval rationale"),
    expiresAt,
    createdAt: timestamp(item.createdAt, "approval time"),
    expired: boolean(item.expired, "approval expiry state"),
  };
}

function findingDecisionEvent(value: unknown): FindingDecisionEvent {
  const item = object(value, "finding decision event");
  const revision = count(item.decisionRevision, "decision event revision");
  if (revision < 2) return invalid("decision event revision");
  const changedFields = array(item.changedFields, "decision changed fields").map((value) =>
    choice(value, ["ownerId", "workflowState", "disposition", "acceptedRiskExpiresAt",
      "dispositionScope", "suppressionExpiresAt", "dispositionRationale"], "decision changed field"));
  if (new Set(changedFields).size !== changedFields.length) return invalid("decision changed fields");
  return {
    id: reportIdentifier(item.id, "decision event"),
    decisionRevision: revision,
    actorId: reportIdentifier(item.actorId, "decision actor"),
    actorName: text(item.actorName, "decision actor name"),
    beforeOwnerName: nullableText(item.beforeOwnerName, "prior owner name"),
    afterOwnerName: nullableText(item.afterOwnerName, "next owner name"),
    action: choice(item.action, ["update", "bulk-update"], "decision action"),
    rationale: text(item.rationale, "decision rationale", true),
    changedFields,
    before: findingDecision(item.before),
    after: findingDecision(item.after),
    createdAt: timestamp(item.createdAt, "decision event time"),
  };
}

function findingCorrelationMember(value: unknown): FindingCorrelationMember {
  const item = object(value, "correlation member");
  return {
    findingId: reportIdentifier(item.findingId, "member finding"),
    sourceId: text(item.sourceId, "member source"),
    title: text(item.title, "member title"),
    severity: choice(item.severity, ["critical", "high", "medium", "low", "info"], "member severity"),
    active: boolean(item.active, "member active state"),
    decisionRevision: count(item.decisionRevision, "member decision revision"),
    evidenceRevision: count(item.evidenceRevision, "member evidence revision"),
    observationCount: count(item.observationCount, "member observation count"),
    noteCount: count(item.noteCount, "member note count"),
    decision: findingDecision(item.decision),
    originalDecision: findingDecision(item.originalDecision),
  };
}

function findingCorrelation(value: unknown, workspace: string | null): FindingCorrelation {
  const item = object(value, "finding correlation");
  const workspaceId = text(item.workspaceId, "correlation workspace");
  if (workspaceId !== workspace) return invalid("correlation workspace");
  const members = uniqueIds(array(item.members, "correlation members").map(findingCorrelationMember)
    .map((member) => ({ ...member, id: member.findingId }))).map(({ id: _id, ...member }) => member);
  const events = uniqueIds(array(item.events, "correlation events").map((value) => {
    const event = object(value, "correlation event");
    return {
      id: reportIdentifier(event.id, "correlation event"),
      type: choice(event.type, ["merge", "split"], "correlation event type"),
      actorId: text(event.actorId, "correlation actor"),
      rationale: text(event.rationale, "correlation rationale"),
      createdAt: timestamp(event.createdAt, "correlation event time"),
      detailAvailability: choice(event.detailAvailability,
        ["available", "archived", "expired", "missing", "corrupt"], "correlation detail availability"),
    };
  }));
  const primaryFindingId = reportIdentifier(item.primaryFindingId, "primary finding");
  const state = choice(item.state, ["active", "split"], "correlation state");
  if (members.length < 2 || !members.some((member) => member.findingId === primaryFindingId)) {
    return invalid("correlation membership");
  }
  const activeMembers = members.filter((member) => member.active);
  if (state === "active" ? activeMembers.length < 2 : activeMembers.length !== 0) {
    return invalid("correlation active membership");
  }
  return {
    id: reportIdentifier(item.id, "correlation identifier"), workspaceId, primaryFindingId,
    state, revision: count(item.revision, "correlation revision"), members, events,
  };
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
    evidenceAvailability: choice(item.evidenceAvailability,
      ["available", "archived", "expired", "missing", "corrupt"], "observation evidence availability"),
    changeKind: item.changeKind === undefined ? "unchanged" : choice(item.changeKind,
      ["new", "changed", "unchanged", "reopened", "inferred-resolved", "historical", "non-authoritative"],
      "observation change kind"),
    changeReasons: item.changeReasons === undefined ? [] :
      array(item.changeReasons, "observation change reasons")
        .map((reason) => text(reason, "observation change reason")),
  };
}

function envelope(value: unknown): { body: Record<string, unknown>; dataOrigin: DataOrigin } {
  const body = object(value, "response");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  return { body, dataOrigin: choice(body.dataOrigin, ["synthetic", "live"], "data origin") };
}

function workItem(value: unknown): WorkItem {
  const item = object(value, "finding");
  const result: WorkItem = {
    id: text(item.id, "finding identifier"),
    title: text(item.title, "finding title"),
    assetName: text(item.assetName, "asset name"),
    severity: choice(item.severity, ["critical", "high", "medium", "low", "info"], "severity"),
    ownerName: item.ownerName === null ? null : text(item.ownerName, "owner name"),
    workflowState: choice(item.workflowState, ["open", "in-progress", "pending-retest", "resolved"], "workflow state"),
    sourceScanAt: item.sourceScanAt === null ? null : timestamp(item.sourceScanAt, "source scan timestamp"),
    collectedAt: timestamp(item.collectedAt, "collection timestamp"),
    importedAt: timestamp(item.importedAt, "import timestamp"),
    changeKind: item.changeKind === undefined ? "unchanged" : choice(item.changeKind,
      ["new", "changed", "unchanged", "reopened", "inferred-resolved"], "finding change kind"),
    changeAt: item.changeAt === undefined ? null : nullableTimestamp(item.changeAt, "finding change time"),
    decisionRevision: count(item.decisionRevision, "finding decision revision"),
    disposition: choice(item.disposition,
      ["none", "accepted-risk", "suppressed", "false-positive"], "finding disposition"),
    acceptedRiskExpiresAt: nullableTimestamp(item.acceptedRiskExpiresAt, "risk acceptance expiry"),
    riskAcceptanceExpired: boolean(item.riskAcceptanceExpired, "risk acceptance expiry state"),
  };
  if (result.decisionRevision < 1 ||
    result.disposition !== "accepted-risk" &&
      (result.acceptedRiskExpiresAt !== null || result.riskAcceptanceExpired)) {
    return invalid("finding decision metadata");
  }
  return result;
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
    changeMode: body.changeMode === undefined ? "all" :
      choice(body.changeMode, ["all", "meaningful"], "work change mode"),
  };
}

function validateWorkContinuation(page: WorkResponse, cursor: string): WorkResponse {
  if (page.items.length > 100) return invalid("finding page size");
  if (page.nextCursor !== null && (!/^[a-f0-9]{32}$/.test(page.nextCursor) ||
    page.nextCursor <= cursor || page.nextCursor !== page.items.at(-1)?.id)) return invalid("finding pagination cursor");
  if (page.items.some((item, index) => !/^[a-f0-9]{32}$/.test(item.id) ||
    item.id <= (index === 0 ? cursor : page.items[index - 1].id))) return invalid("finding page order");
  return page;
}

export function workSearchQuery(draft: string): string {
  const query = draft.trim();
  if (query.includes("\0")) {
    throw new APIError("A workspace search must be NUL-free. Your draft has not been changed.", "invalid-input", false);
  }
  if (new TextEncoder().encode(query).byteLength > 512) {
    throw new APIError("A workspace search must be at most 512 UTF-8 bytes after trimming. Your draft has not been changed.", "invalid-input", false);
  }
  return query;
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
      dispositionApproval: finding.dispositionApproval === undefined ? undefined :
        finding.dispositionApproval === null ? null : findingDispositionApproval(finding.dispositionApproval),
      verifiedResolution: finding.verifiedResolution === undefined ? undefined : boolean(finding.verifiedResolution, "resolution verification"),
      evidenceRevision: finding.evidenceRevision === undefined ? undefined : count(finding.evidenceRevision, "finding evidence revision"),
      changeRevision: finding.changeRevision === undefined ? undefined : count(finding.changeRevision, "finding change revision"),
      correlation: finding.correlation === undefined ? undefined : findingCorrelation(finding.correlation, text(finding.workspaceId, "finding workspace")),
      notes: finding.notes === undefined ? undefined : uniqueIds(array(finding.notes, "analyst notes").map((value) => {
        const note = object(value, "analyst note");
        return { id: text(note.id, "note identifier"), text: text(note.text, "note text", true) };
      })),
      observations: finding.observations === undefined ? undefined : uniqueIds(array(finding.observations, "observations").map(observation)),
      decisionEvents: finding.decisionEvents === undefined ? undefined :
        uniqueIds(array(finding.decisionEvents, "decision events").map(findingDecisionEvent)),
      notesNextCursor: finding.notesNextCursor === undefined ? undefined : nullableText(finding.notesNextCursor, "notes cursor"),
      observationsNextCursor: finding.observationsNextCursor === undefined ? undefined : nullableText(finding.observationsNextCursor, "observations cursor"),
      decisionEventsNextCursor: finding.decisionEventsNextCursor === undefined ? undefined :
        nullableText(finding.decisionEventsNextCursor, "decision events cursor"),
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
    finding.dispositionApproval === undefined ||
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

function decisionHistoryCursor(value: string | undefined): string {
  if (value === undefined) return "";
  if (typeof value !== "string" || (value !== "" && !/^\d{20}$/.test(value))) {
    throw new APIError("Decision history requires a native fixed-width revision cursor.", "invalid-input", false);
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

function validateDecisionHistoryPage(items: readonly FindingDecisionEvent[] | undefined,
  next: string | null | undefined, cursor: string) {
  if ((items !== undefined && items.length > 100) ||
    (cursor !== "" && (items === undefined || next === undefined)) ||
    (next != null && (!/^\d{20}$/.test(next) ||
      next !== items?.at(-1)?.decisionRevision.toString().padStart(20, "0")))) {
    return invalid("decision history page");
  }
  const prior = cursor === "" ? 0n : BigInt(cursor);
  if ((cursor !== "" || next != null) && items?.some((item, index) =>
    BigInt(item.decisionRevision) <= (index === 0 ? prior : BigInt(items[index - 1].decisionRevision)))) {
    return invalid("decision history order");
  }
}

function parseBulkFindingUpdate(value: unknown, ids: readonly string[]): FindingBulkResponse {
  const { body, dataOrigin } = envelope(value);
  const items = uniqueIds(array(body.items, "bulk finding results").map(workItem));
  const total = count(body.total, "bulk finding count");
  const expected = [...ids].sort();
  if (body.nextCursor !== null || total !== items.length || items.length !== expected.length ||
    items.some((item, index) => item.id !== expected[index])) return invalid("bulk finding response");
  return { apiVersion, dataOrigin, items, total, nextCursor: null };
}

function findingBodyLimit(body: unknown, bytes: number): void {
  if (new TextEncoder().encode(JSON.stringify(body)).byteLength > bytes) {
    throw new APIError("The encoded finding action exceeds the service's request size limit. Shorten the input and retry.", "invalid-input", false);
  }
}

function parseMergePreview(value: unknown, workspace: string | null, primary: string, other: string): FindingMergePreviewResponse {
  const preview = object(versioned(value).mergePreview, "merge preview");
  const primaryMember = findingCorrelationMember(preview.primary);
  const otherMember = findingCorrelationMember(preview.other);
  if (primaryMember.findingId !== primary || otherMember.findingId !== other) return invalid("merge preview binding");
  const conflicts = array(preview.conflicts, "merge conflicts").map((value) =>
  choice(value, ["ownerId", "workflowState", "disposition", "acceptedRiskExpiresAt",
    "dispositionScope", "suppressionExpiresAt", "dispositionRationale"], "merge conflict"));
  const correlation = preview.correlation === null ? null : findingCorrelation(preview.correlation, workspace);
  if (correlation !== null && (correlation.state !== "active" || correlation.primaryFindingId !== primary ||
    correlation.members.some((member) => member.findingId === other))) return invalid("merge correlation binding");
  return { apiVersion, mergePreview: { primary: primaryMember, other: otherMember, conflicts, correlation } };
}

function parseCorrelationCandidates(value: unknown, workspace: string | null, cursor: string,
  limit: number): FindingCorrelationCandidatesResponse {
  const body = object(versioned(value).correlationCandidates, "correlation candidates");
  const items = array(body.items, "correlation candidate items").map((value) => {
    const item = object(value, "correlation candidate");
    const member = findingCorrelationMember(item.member);
    const match = object(item.match, "correlation candidate match");
    const line = count(match.line, "candidate line");
    const uri = text(match.uri, "candidate URI");
    const branch = text(match.branch, "candidate branch");
    if (!member.active || line < 1 || uri.trim() !== uri || branch.trim() !== branch) {
      return invalid("correlation candidate");
    }
    return {
      member,
      match: { kind: choice(match.kind, ["exact-location"], "candidate match kind"), branch, uri, line },
    };
  });
  const ids = items.map((item) => item.member.findingId);
  if (items.length > limit || ids.some((id, index) => id <= (index === 0 ? cursor : ids[index - 1]))) {
    return invalid("correlation candidate order");
  }
  const nextCursor = body.nextCursor === null ? null : reportIdentifier(body.nextCursor, "candidate cursor");
  if (nextCursor !== null && nextCursor !== ids.at(-1)) return invalid("correlation candidate cursor");
  void workspace;
  return { apiVersion, correlationCandidates: { items, nextCursor } };
}

function parseSplitPreview(value: unknown, workspace: string | null, primary: string, member: string): FindingSplitPreviewResponse {
  const preview = object(versioned(value).splitPreview, "split preview");
  const correlation = findingCorrelation(preview.correlation, workspace);
  const primaryMember = findingCorrelationMember(preview.primary);
  const splitMember = findingCorrelationMember(preview.member);
  if (correlation.primaryFindingId !== primary || primaryMember.findingId !== primary ||
    splitMember.findingId !== member || correlation.state !== "active") return invalid("split preview binding");
  return { apiVersion, splitPreview: { correlation, primary: primaryMember, member: splitMember } };
}

function parseCorrelationResponse(value: unknown, workspace: string | null): FindingCorrelationResponse {
  return { apiVersion, correlation: findingCorrelation(versioned(value).correlation, workspace) };
}

function retentionPolicy(value: unknown, workspace: string | null) {
  const item = object(value, "retention policy");
  const workspaceId = text(item.workspaceId, "retention policy workspace");
  if (workspaceId !== workspace) return invalid("retention policy workspace");
  const result = {
    workspaceId,
    revision: count(item.revision, "retention policy revision"),
    hotHistoryDays: count(item.hotHistoryDays, "hot history retention"),
    rawReportDays: count(item.rawReportDays, "raw report retention"),
    archivedEvidenceDays: count(item.archivedEvidenceDays, "archived evidence retention"),
    auditDays: count(item.auditDays, "audit retention"),
    updatedBy: nullableText(item.updatedBy, "retention policy author"),
    updatedAt: nullableTimestamp(item.updatedAt, "retention policy update time"),
  };
  if (result.revision < 1 || result.hotHistoryDays < 1 ||
    result.hotHistoryDays >= result.rawReportDays ||
    result.rawReportDays >= result.archivedEvidenceDays ||
    result.archivedEvidenceDays >= result.auditDays ||
    (result.updatedBy === null) !== (result.updatedAt === null)) return invalid("retention policy ordering");
  return result;
}

function parseRetentionPolicy(value: unknown, workspace: string | null): RetentionPolicyResponse {
  return { apiVersion, retentionPolicy: retentionPolicy(versioned(value).retentionPolicy, workspace) };
}

function retentionHold(value: unknown, workspace: string | null): RetentionHold {
  const item = object(value, "retention hold");
  const workspaceId = text(item.workspaceId, "retention hold workspace");
  if (workspaceId !== workspace) return invalid("retention hold workspace");
  const result: RetentionHold = {
    id: reportIdentifier(item.id, "retention hold"),
    workspaceId,
    resourceKind: choice(item.resourceKind, [
      "import", "observation", "correlation-event", "finding-decision-event",
      "notification-policy-revision", "finding-change-event", "notification-policy-event",
    ], "retention resource kind"),
    resourceId: reportIdentifier(item.resourceId, "retention resource"),
    reason: text(item.reason, "retention hold reason"),
    revision: count(item.revision, "retention hold revision"),
    createdBy: text(item.createdBy, "retention hold creator"),
    createdAt: timestamp(item.createdAt, "retention hold creation time"),
    releasedBy: nullableText(item.releasedBy, "retention hold release actor"),
    releasedAt: nullableTimestamp(item.releasedAt, "retention hold release time"),
    releaseRationale: nullableText(item.releaseRationale, "retention hold release rationale"),
  };
  if (result.revision < 1 || (result.releasedAt === null) !== (result.releasedBy === null) ||
    (result.releasedAt === null) !== (result.releaseRationale === null)) return invalid("retention hold lifecycle");
  return result;
}

function parseRetentionHold(value: unknown, workspace: string | null): RetentionHoldResponse {
  return { apiVersion, retentionHold: retentionHold(versioned(value).retentionHold, workspace) };
}

function parseRetentionHolds(value: unknown, workspace: string | null): RetentionHoldsResponse {
  const holds = uniqueIds(array(versioned(value).retentionHolds, "retention holds")
    .map((item) => retentionHold(item, workspace)));
  return { apiVersion, retentionHolds: holds };
}

function retentionSummary(value: unknown): RetentionClassSummary {
  const item = object(value, "retention summary");
  const result: RetentionClassSummary = {
    class: choice(item.class,
      ["hot-history", "archived-evidence", "raw-report", "audit", "orphan-archive"], "retention class"),
    action: choice(item.action,
      ["archive-history", "expire-archive", "expire-raw-report", "archive-audit", "delete-orphan"],
      "retention action"),
    retainDays: count(item.retainDays, "retention days"),
    totalCount: count(item.totalCount, "retention total"),
    eligibleCount: count(item.eligibleCount, "retention eligible count"),
    protectedCount: count(item.protectedCount, "retention protected count"),
    sizeBytes: count(item.sizeBytes, "retention bytes"),
  };
  const actions: Record<RetentionClassSummary["class"], RetentionClassSummary["action"]> = {
    "hot-history": "archive-history", "archived-evidence": "expire-archive",
    "raw-report": "expire-raw-report", audit: "archive-audit", "orphan-archive": "delete-orphan",
  };
  if (result.retainDays < 1 || result.eligibleCount + result.protectedCount !== result.totalCount ||
    actions[result.class] !== result.action) return invalid("retention summary consistency");
  return result;
}

function retentionPreviewItem(value: unknown): RetentionPreviewItem {
  const item = object(value, "retention preview item");
  const result: RetentionPreviewItem = {
    class: choice(item.class, ["hot-history", "archived-evidence", "raw-report", "audit", "orphan-archive"], "retention item class"),
    resourceKind: choice(item.resourceKind, [
      "import", "observation", "correlation-event", "archive-object",
      "finding-decision-event", "notification-policy-revision",
      "finding-change-event", "notification-policy-event",
    ], "retention item kind"),
    resourceId: reportIdentifier(item.resourceId, "retention item resource"),
    action: choice(item.action,
      ["archive-history", "expire-archive", "expire-raw-report", "archive-audit", "delete-orphan"],
      "retention item action"),
    observedAt: timestamp(item.observedAt, "retention item time"),
    sizeBytes: count(item.sizeBytes, "retention item bytes"),
    protectedReasons: array(item.protectedReasons, "retention protection reasons").map((reason) =>
      choice(reason, ["legal-hold", "active-decision", "shared-observation-references",
        "assessment-reference", "active-correlation", "archive-reference",
        "current-policy-revision", "pending-policy-evaluation", "active-delivery"],
      "retention protection reason")),
    objectKey: item.objectKey === undefined ? null : nullableText(item.objectKey, "retention object key"),
    objectDigest: item.objectDigest === undefined ? null : nullableText(item.objectDigest, "retention object digest"),
    objectRevision: item.objectRevision === undefined || item.objectRevision === null
      ? null : count(item.objectRevision, "retention object revision"),
  };
  const actions: Record<RetentionPreviewItem["class"], RetentionPreviewItem["action"]> = {
    "hot-history": "archive-history", "archived-evidence": "expire-archive",
    "raw-report": "expire-raw-report", audit: "archive-audit", "orphan-archive": "delete-orphan",
  };
  if (actions[result.class] !== result.action ||
    (result.resourceKind === "archive-object") !== (result.objectKey !== null) ||
    (result.objectKey === null) !== (result.objectDigest === null) ||
    (result.objectDigest === null) !== (result.objectRevision === null) ||
    new Set(result.protectedReasons).size !== result.protectedReasons.length) {
    return invalid("retention preview item consistency");
  }
  return result;
}

function retentionPreview(value: unknown, workspace: string | null): RetentionPreview {
  const item = object(value, "retention preview");
  const workspaceId = text(item.workspaceId, "retention preview workspace");
  if (workspaceId !== workspace) return invalid("retention preview workspace");
  const summaries = array(item.summaries, "retention summaries").map(retentionSummary);
  if (summaries.length !== 5 || new Set(summaries.map((summary) => summary.class)).size !== 5) {
    return invalid("retention summary classes");
  }
  const items = array(item.items, "retention preview items").map(retentionPreviewItem);
  const itemKeys = items.map((entry) => `${entry.class}:${entry.resourceKind}:${entry.resourceId}`);
  if (new Set(itemKeys).size !== itemKeys.length) return invalid("retention preview item identity");
  const result: RetentionPreview = {
    id: reportIdentifier(item.id, "retention preview"),
    workspaceId,
    revision: count(item.revision, "retention preview revision"),
    state: choice(item.state, ["ready", "approved", "stale"], "retention preview state"),
    policyRevision: count(item.policyRevision, "retention preview policy revision"),
    snapshotDigest: text(item.snapshotDigest, "retention preview digest"),
    createdBy: text(item.createdBy, "retention preview creator"),
    createdAt: timestamp(item.createdAt, "retention preview creation time"),
    expiresAt: timestamp(item.expiresAt, "retention preview expiry"),
    summaries,
    items,
    approvedBy: nullableText(item.approvedBy, "retention approval actor"),
    approvedAt: nullableTimestamp(item.approvedAt, "retention approval time"),
    approvalRationale: nullableText(item.approvalRationale, "retention approval rationale"),
  };
  if (result.revision < 1 || result.policyRevision < 1 ||
    !/^sha256:[a-f0-9]{64}$/.test(result.snapshotDigest) ||
    (result.state === "approved") !== (result.approvedBy !== null) ||
    (result.approvedBy === null) !== (result.approvedAt === null) ||
    (result.approvedAt === null) !== (result.approvalRationale === null)) {
    return invalid("retention preview lifecycle");
  }
  return result;
}

function parseRetentionPreview(value: unknown, workspace: string | null): RetentionPreviewResponse {
  return { apiVersion, retentionPreview: retentionPreview(versioned(value).retentionPreview, workspace) };
}

function retentionFailure(value: unknown, field: string): RetentionRun["failure"] {
  if (value === null) return null;
  const item = object(value, field);
  return {
    code: text(item.code, `${field} code`), message: text(item.message, `${field} message`),
    retryable: boolean(item.retryable, `${field} retry policy`),
  };
}

function retentionRunItem(value: unknown): RetentionRunItem {
  const item = object(value, "retention run item");
  const state = choice(item.state,
    ["queued", "processing", "succeeded", "protected", "missing", "corrupt", "failed"],
    "retention item state");
  const failure = retentionFailure(item.failure, "retention item failure");
  const completedAt = nullableTimestamp(item.completedAt, "retention item completion time");
  if ((["succeeded", "protected", "missing", "corrupt", "failed"].includes(state)) !== (completedAt !== null) ||
    (state === "failed" && failure === null) ||
    (["succeeded", "protected", "missing", "corrupt"].includes(state) && failure !== null) ||
    (failure?.retryable === true && state !== "queued")) return invalid("retention item lifecycle");
  return {
    id: reportIdentifier(item.id, "retention item"),
    class: choice(item.class, ["hot-history", "archived-evidence", "raw-report", "audit", "orphan-archive"], "retention item class"),
    resourceKind: choice(item.resourceKind,
      ["import", "observation", "correlation-event", "archive-object",
        "finding-decision-event", "notification-policy-revision",
        "finding-change-event", "notification-policy-event"], "retention item resource kind"),
    resourceId: reportIdentifier(item.resourceId, "retention item resource"),
    action: choice(item.action,
      ["archive-history", "expire-archive", "expire-raw-report", "archive-audit", "restore-archive", "delete-orphan"],
      "retention item action"),
    state,
    protectedReasons: array(item.protectedReasons, "retention execution protection reasons")
      .map((reason) => text(reason, "retention execution protection reason")),
    outcome: text(item.outcome, "retention item outcome", true),
    failure,
    objectKey: item.objectKey === undefined ? null : nullableText(item.objectKey, "retention execution object key"),
    objectDigest: item.objectDigest === undefined ? null : nullableText(item.objectDigest, "retention execution object digest"),
    objectRevision: item.objectRevision === undefined || item.objectRevision === null
      ? null : count(item.objectRevision, "retention execution object revision"),
    startedAt: nullableTimestamp(item.startedAt, "retention item start time"),
    completedAt,
  };
}

function parseRetentionRun(value: unknown, workspace: string | null): RetentionRunResponse {
  const item = object(versioned(value).retentionRun, "retention run");
  const workspaceId = text(item.workspaceId, "retention run workspace");
  if (workspaceId !== workspace) return invalid("retention run workspace");
  const operation = choice(item.operation, ["apply-preview", "restore-observation"], "retention operation");
  const state = choice(item.state, ["queued", "processing", "succeeded", "partial", "failed"], "retention run state");
  const completedAt = nullableTimestamp(item.completedAt, "retention run completion time");
  const failure = retentionFailure(item.failure, "retention run failure");
  const items = uniqueIds(array(item.items, "retention run items").map(retentionRunItem));
  const result: RetentionRun = {
    id: reportIdentifier(item.id, "retention run"), workspaceId, operation,
    previewId: item.previewId === null ? null : reportIdentifier(item.previewId, "retention preview"),
    targetKind: item.targetKind === null ? null : choice(item.targetKind, ["observation"], "retention target kind"),
    targetId: item.targetId === null ? null : reportIdentifier(item.targetId, "retention target"),
    state, requestedBy: text(item.requestedBy, "retention requester"),
    rationale: text(item.rationale, "retention rationale"),
    createdAt: timestamp(item.createdAt, "retention creation time"), completedAt,
    total: count(item.total, "retention item total"), succeeded: count(item.succeeded, "retention succeeded count"),
    protected: count(item.protected, "retention protected count"), missing: count(item.missing, "retention missing count"),
    corrupt: count(item.corrupt, "retention corrupt count"), failed: count(item.failed, "retention failed count"),
    failure, items,
  };
  if ((["succeeded", "partial", "failed"].includes(state)) !== (completedAt !== null) ||
    (state === "failed") !== (failure !== null) || result.total !== items.length ||
    result.succeeded + result.protected + result.missing + result.corrupt + result.failed > result.total ||
    (operation === "apply-preview") !== (result.previewId !== null) ||
    (operation === "restore-observation") !== (result.targetKind === "observation" && result.targetId !== null)) {
    return invalid("retention run lifecycle");
  }
  return { apiVersion, retentionRun: result };
}

function archivedHistory(value: unknown, workspace: string | null,
  expectedKind: HistoryRetentionResourceKind, expectedId: string): ArchivedHistory {
  const item = object(value, "archived history response");
  const resourceKind = choice(item.resourceKind, [
    "finding-decision-event", "notification-policy-revision",
    "finding-change-event", "notification-policy-event",
  ], "archived history resource kind");
  const availability = choice(item.availability, ["archived"], "archived history availability");
  const resourceId = reportIdentifier(item.resourceId, "archived history resource");
  const digest = text(item.digest, "archived history archive reference");
  const sizeBytes = count(item.sizeBytes, "archived history archive reference");
  const detailRevision = count(item.detailRevision, "archived history detail revision");
  const filename = text(item.filename, "archived history filename");
  const body = text(item.text, "archived history body", true);
  const actualDigest = text(item.actualDigest, "archived history archive reference");
  const contentLength = count(item.contentLength, "archived history archive reference");
  if (resourceKind !== expectedKind || resourceId !== expectedId) {
    return invalid("archived history resource identity");
  }
  if (!/^sha256:[a-f0-9]{64}$/.test(digest) || digest !== actualDigest ||
    sizeBytes !== new TextEncoder().encode(body).byteLength || contentLength !== sizeBytes ||
    detailRevision < 2 ||
    filename !== `history-${resourceKind}-${resourceId}.json`) {
    return invalid("archived history archive reference");
  }
  let payload: Record<string, unknown>;
  try {
    payload = object(JSON.parse(body), "archived history JSON");
  } catch {
    return invalid("archived history JSON");
  }
  if (payload.schemaVersion !== 1 || payload.resourceKind !== resourceKind ||
    payload.id !== resourceId || payload.workspaceId !== workspace) {
    return invalid("archived history resource identity");
  }
  return { resourceKind, resourceId, availability, digest, sizeBytes, detailRevision, filename, text: body };
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

export function parseImportReceipt(value: unknown): ImportReceipt {
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
    reportDigest: text(item.reportDigest, "report digest"),
    evidenceAvailability: choice(item.evidenceAvailability,
      ["available", "archived", "expired", "missing", "corrupt"], "import evidence availability"),
    observationCount: count(item.observationCount, "observation count"), failure,
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
      suppressed: count(totals.suppressed, "suppressed finding total"),
      expiredSuppression: count(totals.expiredSuppression, "expired suppression total"),
      falsePositive: count(totals.falsePositive, "false-positive total"),
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

const trendVerificationReason = "Historical snapshot trends are not an SLA, forecast, or independent verification.";
const coverageVerificationReason = "Coverage drill-down reflects successful complete full-scan intake; it is not verification of asset safety.";
const findingMetricVerificationReason = "Finding metric drill-down reflects current canonical finding state; it does not verify safety or historical membership.";
const remediationSLAVerificationReason = "Remediation SLA status is a time-to-workflow target; it does not verify safety, resolution, or risk acceptance.";
const trendTotalKeys = ["assets", "findings", "openFindings", "acceptedRisk", "expiredAcceptedRisk",
  "suppressed", "expiredSuppression", "falsePositive", "inferredResolved", "verifiedResolved"] as const;
const trendSeverityKeys = ["critical", "high", "medium", "low", "info"] as const;
const trendCoverageKeys = ["scannedAssets", "unscannedAssets", "staleAssets",
  "unknownFreshnessAssets", "freshnessWindowDays"] as const;
const trendDeltaKeys = ["findings", "openFindings", "acceptedRisk", "suppressed", "falsePositive",
  "critical", "high", "medium", "low", "info", "scannedAssets", "unscannedAssets",
  "staleAssets", "unknownFreshnessAssets"] as const;

function trendPoint(value: unknown): HistoricalTrendPoint {
  const item = exactObject(value, "historical trend point",
    ["snapshotId", "name", "completedAt", "asOf", "totals", "bySeverity", "coverage"]);
  const totals = exactObject(item.totals, "historical trend totals", trendTotalKeys);
  const severity = exactObject(item.bySeverity, "historical trend severity", trendSeverityKeys);
  const coverage = exactObject(item.coverage, "historical trend coverage", trendCoverageKeys);
  const result: HistoricalTrendPoint = {
    snapshotId: reportIdentifier(item.snapshotId, "historical trend snapshot"),
    name: text(item.name, "historical trend snapshot name"),
    completedAt: timestamp(item.completedAt, "historical trend completion time"),
    asOf: timestamp(item.asOf, "historical trend as-of time"),
    totals: {
      assets: count(totals.assets, "historical trend assets"),
      findings: count(totals.findings, "historical trend findings"),
      openFindings: count(totals.openFindings, "historical trend open findings"),
      acceptedRisk: count(totals.acceptedRisk, "historical trend accepted risk"),
      expiredAcceptedRisk: count(totals.expiredAcceptedRisk, "historical trend expired accepted risk"),
      suppressed: count(totals.suppressed, "historical trend suppressed"),
      expiredSuppression: count(totals.expiredSuppression, "historical trend expired suppression"),
      falsePositive: count(totals.falsePositive, "historical trend false positives"),
      inferredResolved: count(totals.inferredResolved, "historical trend inferred resolved"),
      verifiedResolved: count(totals.verifiedResolved, "historical trend verified resolved"),
    },
    bySeverity: {
      critical: count(severity.critical, "historical trend critical"),
      high: count(severity.high, "historical trend high"),
      medium: count(severity.medium, "historical trend medium"),
      low: count(severity.low, "historical trend low"),
      info: count(severity.info, "historical trend info"),
    },
    coverage: {
      scannedAssets: count(coverage.scannedAssets, "historical trend scanned assets"),
      unscannedAssets: count(coverage.unscannedAssets, "historical trend unscanned assets"),
      staleAssets: count(coverage.staleAssets, "historical trend stale assets"),
      unknownFreshnessAssets: count(coverage.unknownFreshnessAssets, "historical trend unknown freshness"),
      freshnessWindowDays: reportFreshness(coverage.freshnessWindowDays),
    },
  };
  if (Date.parse(result.asOf) !== Date.parse(result.completedAt) ||
    Object.values(result.bySeverity).reduce((sum, item) => sum + item, 0) !== result.totals.findings ||
    result.coverage.scannedAssets + result.coverage.unscannedAssets !== result.totals.assets ||
    result.coverage.staleAssets > result.coverage.scannedAssets ||
    result.coverage.unknownFreshnessAssets > result.coverage.scannedAssets) {
    return invalid("historical trend point consistency");
  }
  return result;
}

function trendDelta(value: unknown): HistoricalTrendDelta {
  const item = exactObject(value, "historical trend delta", trendDeltaKeys);
  const metric = (key: typeof trendDeltaKeys[number]) =>
    signedInteger(item[key], `historical trend ${key} delta`);
  return {
    findings: metric("findings"),
    openFindings: metric("openFindings"),
    acceptedRisk: metric("acceptedRisk"),
    suppressed: metric("suppressed"),
    falsePositive: metric("falsePositive"),
    critical: metric("critical"),
    high: metric("high"),
    medium: metric("medium"),
    low: metric("low"),
    info: metric("info"),
    scannedAssets: metric("scannedAssets"),
    unscannedAssets: metric("unscannedAssets"),
    staleAssets: metric("staleAssets"),
    unknownFreshnessAssets: metric("unknownFreshnessAssets"),
  };
}

function parseHistoricalTrend(value: unknown, workspace: string | null, days: number): HistoricalTrendResponse {
  const { body, dataOrigin } = envelope(value);
  if (dataOrigin !== "live") return invalid("historical trend data origin");
  const trend = exactObject(body.trend, "historical trend",
    ["workspaceId", "from", "to", "days", "points", "delta", "verification"]);
  const workspaceId = text(trend.workspaceId, "historical trend workspace");
  const from = timestamp(trend.from, "historical trend from");
  const to = timestamp(trend.to, "historical trend to");
  const parsedDays = count(trend.days, "historical trend days");
  if (workspaceId !== workspace || parsedDays !== days ||
    Date.parse(to) - Date.parse(from) !== days * 24 * 60 * 60 * 1000) {
    return invalid("historical trend window");
  }
  const points = array(trend.points, "historical trend points").map(trendPoint);
  if (new Set(points.map((point) => point.snapshotId)).size !== points.length ||
    points.length > 100 || points.some((point, index) =>
    Date.parse(point.completedAt) < Date.parse(from) || Date.parse(point.completedAt) > Date.parse(to) ||
    index > 0 && (Date.parse(point.completedAt) < Date.parse(points[index - 1].completedAt) ||
      Date.parse(point.completedAt) === Date.parse(points[index - 1].completedAt) &&
      point.snapshotId <= points[index - 1].snapshotId))) {
    return invalid("historical trend point order");
  }
  const delta = trend.delta === null ? null : trendDelta(trend.delta);
  if ((points.length < 2) !== (delta === null)) return invalid("historical trend delta presence");
  if (delta !== null) {
    const first = points[0], last = points[points.length - 1];
    const expected: HistoricalTrendDelta = {
      findings: last.totals.findings - first.totals.findings,
      openFindings: last.totals.openFindings - first.totals.openFindings,
      acceptedRisk: last.totals.acceptedRisk - first.totals.acceptedRisk,
      suppressed: last.totals.suppressed - first.totals.suppressed,
      falsePositive: last.totals.falsePositive - first.totals.falsePositive,
      critical: last.bySeverity.critical - first.bySeverity.critical,
      high: last.bySeverity.high - first.bySeverity.high,
      medium: last.bySeverity.medium - first.bySeverity.medium,
      low: last.bySeverity.low - first.bySeverity.low,
      info: last.bySeverity.info - first.bySeverity.info,
      scannedAssets: last.coverage.scannedAssets - first.coverage.scannedAssets,
      unscannedAssets: last.coverage.unscannedAssets - first.coverage.unscannedAssets,
      staleAssets: last.coverage.staleAssets - first.coverage.staleAssets,
      unknownFreshnessAssets: last.coverage.unknownFreshnessAssets - first.coverage.unknownFreshnessAssets,
    };
    if (trendDeltaKeys.some((key) => delta[key] !== expected[key])) return invalid("historical trend delta arithmetic");
  }
  const verification = exactObject(trend.verification, "historical trend verification", ["state", "reason"]);
  if (verification.state !== "not-run" || verification.reason !== trendVerificationReason) {
    return invalid("historical trend verification");
  }
  return {
    apiVersion, dataOrigin: "live",
    trend: { workspaceId, from, to, days, points, delta,
      verification: { state: "not-run", reason: trendVerificationReason } },
  };
}

function coverageAssetItem(value: unknown, workspace: string | null, to: string): CoverageAssetItem {
  const item = exactObject(value, "coverage asset item", ["asset", "coverage"]);
  exactObject(item.asset, "coverage asset",
    ["id", "workspaceId", "name", "kind", "environment", "criticality", "tags", "ownerId"]);
  const coverage = exactObject(item.coverage, "coverage asset flags",
    ["scanned", "stale", "unknownFreshness", "latestSourceScanAt"]);
  const result: CoverageAssetItem = {
    asset: asset(item.asset, workspace),
    coverage: {
      scanned: boolean(coverage.scanned, "coverage scanned state"),
      stale: boolean(coverage.stale, "coverage stale state"),
      unknownFreshness: boolean(coverage.unknownFreshness, "coverage unknown freshness state"),
      latestSourceScanAt: nullableTimestamp(coverage.latestSourceScanAt, "coverage latest source scan time"),
    },
  };
  if (!/^[a-f0-9]{32}$/.test(result.asset.id) ||
    result.asset.ownerId !== null && !/^[a-f0-9]{32}$/.test(result.asset.ownerId) ||
    !result.coverage.scanned && (result.coverage.stale || result.coverage.unknownFreshness ||
      result.coverage.latestSourceScanAt !== null) ||
    (result.coverage.stale || result.coverage.unknownFreshness) && !result.coverage.scanned ||
    result.coverage.scanned && !result.coverage.unknownFreshness &&
      result.coverage.latestSourceScanAt === null ||
    result.coverage.latestSourceScanAt !== null &&
      Date.parse(result.coverage.latestSourceScanAt) > Date.parse(to)) {
    return invalid("coverage asset consistency");
  }
  return result;
}

function parseCoverageAssetDrilldown(value: unknown, workspace: string | null,
  state: CoverageAssetState, days: number, limit: number, cursor: string): CoverageAssetDrilldownResponse {
  const body = exactObject(value, "coverage drill-down response", ["apiVersion", "dataOrigin", "drilldown"]);
  if (body.apiVersion !== apiVersion || body.dataOrigin !== "live") return invalid("coverage drill-down envelope");
  const drilldown = exactObject(body.drilldown, "coverage drill-down",
    ["workspaceId", "state", "freshnessWindow", "items", "total", "nextCursor", "verification"]);
  const workspaceId = text(drilldown.workspaceId, "coverage drill-down workspace");
  const parsedState = choice(drilldown.state,
    ["scanned", "unscanned", "stale", "unknown-freshness"], "coverage drill-down state");
  const window = exactObject(drilldown.freshnessWindow, "coverage freshness window", ["from", "to", "days"]);
  const from = timestamp(window.from, "coverage freshness start");
  const to = timestamp(window.to, "coverage freshness end");
  const parsedDays = count(window.days, "coverage freshness days");
  if (workspaceId !== workspace || parsedState !== state || parsedDays !== days ||
    Date.parse(to) - Date.parse(from) !== days * 24 * 60 * 60 * 1000) {
    return invalid("coverage drill-down authority or window");
  }
  const items = array(drilldown.items, "coverage asset list")
    .map((value) => coverageAssetItem(value, workspace, to));
  const total = count(drilldown.total, "coverage asset total");
  const nextCursor = nullableText(drilldown.nextCursor, "coverage asset cursor");
  if (items.length > limit || total < items.length ||
    new Set(items.map((item) => item.asset.id)).size !== items.length ||
    items.some((item, index) =>
      item.asset.id <= (index === 0 ? cursor : items[index - 1].asset.id))) {
    return invalid("coverage asset page");
  }
  const member = (item: CoverageAssetItem) => state === "scanned" ? item.coverage.scanned :
    state === "unscanned" ? !item.coverage.scanned :
      state === "stale" ? item.coverage.stale : item.coverage.unknownFreshness;
  if (items.some((item) => !member(item))) return invalid("coverage asset membership");
  if (nextCursor !== null && (!/^[a-f0-9]{32}$/.test(nextCursor) ||
    items.length !== limit || nextCursor !== items.at(-1)?.asset.id || nextCursor <= cursor) ||
    items.length < limit && nextCursor !== null ||
    cursor === "" && (total > items.length) !== (nextCursor !== null)) {
    return invalid("coverage asset pagination");
  }
  const verification = exactObject(drilldown.verification, "coverage drill-down verification", ["state", "reason"]);
  if (verification.state !== "not-run" || verification.reason !== coverageVerificationReason) {
    return invalid("coverage drill-down verification");
  }
  return {
    apiVersion, dataOrigin: "live",
    drilldown: {
      workspaceId, state, freshnessWindow: { from, to, days },
      items, total, nextCursor,
      verification: { state: "not-run", reason: coverageVerificationReason },
    },
  };
}

function findingMetricTotal(report: PostureReport, metric: FindingMetric) {
  switch (metric) {
    case "findings": return report.totals.findings;
    case "open-findings": return report.totals.openFindings;
    case "accepted-risk": return report.totals.acceptedRisk;
    case "expired-accepted-risk": return report.totals.expiredAcceptedRisk;
    case "suppressed": return report.totals.suppressed;
    case "expired-suppression": return report.totals.expiredSuppression;
    case "false-positive": return report.totals.falsePositive;
    case "inferred-resolved": return report.totals.inferredResolved;
    case "critical":
    case "high":
    case "medium":
    case "low":
    case "info":
      return report.bySeverity[metric];
  }
}

function findingMetricItem(value: unknown, metric: FindingMetric, asOf: string): FindingMetricItem {
  const item = exactObject(value, "finding metric item", [
    "findingId", "title", "assetId", "assetName", "severity", "ownerId", "ownerName",
    "workflowState", "disposition", "acceptedRiskExpiresAt", "riskAcceptanceExpired",
    "sourceState", "sourceFreshnessAt",
  ]);
  const result: FindingMetricItem = {
    findingId: reportIdentifier(item.findingId, "finding metric finding"),
    title: text(item.title, "finding metric title"),
    assetId: reportIdentifier(item.assetId, "finding metric asset"),
    assetName: text(item.assetName, "finding metric asset name"),
    severity: choice(item.severity, ["critical", "high", "medium", "low", "info"], "finding metric severity"),
    ownerId: nullableText(item.ownerId, "finding metric owner"),
    ownerName: nullableText(item.ownerName, "finding metric owner name"),
    workflowState: choice(item.workflowState,
      ["open", "in-progress", "pending-retest", "resolved"], "finding metric workflow"),
    disposition: choice(item.disposition,
      ["none", "accepted-risk", "suppressed", "false-positive"], "finding metric disposition"),
    acceptedRiskExpiresAt: nullableTimestamp(item.acceptedRiskExpiresAt, "accepted-risk expiry"),
    riskAcceptanceExpired: boolean(item.riskAcceptanceExpired, "accepted-risk expired state"),
    sourceState: choice(item.sourceState,
      ["observed", "inferred-resolved", "stale", "unknown"], "finding metric source state"),
    sourceFreshnessAt: nullableTimestamp(item.sourceFreshnessAt, "finding metric source freshness"),
  };
  const expired = result.disposition === "accepted-risk" && result.acceptedRiskExpiresAt !== null &&
    Date.parse(result.acceptedRiskExpiresAt) <= Date.parse(asOf);
  const member = metric === "findings" ? true :
    metric === "open-findings" ? result.workflowState !== "resolved" :
      metric === "accepted-risk" ? result.disposition === "accepted-risk" :
        metric === "expired-accepted-risk" ? expired :
          metric === "suppressed" || metric === "expired-suppression" ? result.disposition === "suppressed" :
            metric === "false-positive" ? result.disposition === "false-positive" :
              metric === "inferred-resolved" ? result.sourceState === "inferred-resolved" :
                result.severity === metric;
  if ((result.ownerId === null) !== (result.ownerName === null) ||
    result.ownerId !== null && !/^[a-f0-9]{32}$/.test(result.ownerId) ||
    result.disposition !== "accepted-risk" &&
      (result.acceptedRiskExpiresAt !== null || result.riskAcceptanceExpired) ||
    result.riskAcceptanceExpired !== expired || !member) {
    return invalid("finding metric membership");
  }
  return result;
}

function parseFindingMetric(value: unknown, workspace: string | null, metric: FindingMetric,
  limit: number, cursor: string, report: PostureReport): FindingMetricResponse {
  const body = exactObject(value, "finding metric response", ["apiVersion", "dataOrigin", "drilldown"]);
  if (body.apiVersion !== apiVersion || body.dataOrigin !== "live") return invalid("finding metric envelope");
  const drilldown = exactObject(body.drilldown, "finding metric drill-down",
    ["workspaceId", "metric", "asOf", "items", "total", "nextCursor", "verification"]);
  const workspaceId = text(drilldown.workspaceId, "finding metric workspace");
  const parsedMetric = choice(drilldown.metric, [
    "findings", "open-findings", "accepted-risk", "expired-accepted-risk",
    "suppressed", "expired-suppression", "false-positive", "inferred-resolved",
    "critical", "high", "medium", "low", "info",
  ], "finding metric");
  const asOf = timestamp(drilldown.asOf, "finding metric as-of");
  if (workspaceId !== workspace || parsedMetric !== metric || asOf !== report.asOf) {
    return invalid("finding metric authority");
  }
  const items = array(drilldown.items, "finding metric items")
    .map((item) => findingMetricItem(item, metric, asOf));
  const total = count(drilldown.total, "finding metric total");
  const expectedTotal = findingMetricTotal(report, metric);
  if (total !== expectedTotal) {
    throw new APIError("Finding membership changed. Refresh Live overview before loading replacement data.",
      "invalid-response", false);
  }
  const nextCursor = nullableText(drilldown.nextCursor, "finding metric cursor");
  if (items.length > limit || total < items.length ||
    new Set(items.map((item) => item.findingId)).size !== items.length ||
    items.some((item, index) =>
      item.findingId <= (index === 0 ? cursor : items[index - 1].findingId))) {
    return invalid("finding metric page order");
  }
  if (nextCursor !== null && (!/^[a-f0-9]{32}$/.test(nextCursor) ||
    items.length !== limit || nextCursor !== items.at(-1)?.findingId || nextCursor <= cursor) ||
    items.length < limit && nextCursor !== null ||
    cursor === "" && (total > items.length) !== (nextCursor !== null)) {
    return invalid("finding metric pagination");
  }
  const verification = exactObject(drilldown.verification, "finding metric verification", ["state", "reason"]);
  if (verification.state !== "not-run" || verification.reason !== findingMetricVerificationReason) {
    return invalid("finding metric verification");
  }
  return {
    apiVersion, dataOrigin: "live",
    drilldown: {
      workspaceId, metric, asOf, items, total, nextCursor,
      verification: { state: "not-run", reason: findingMetricVerificationReason },
    },
  };
}

const slaPolicyKeys = ["workspaceId", "criticalDays", "highDays", "mediumDays", "lowDays",
  "infoDays", "revision", "approvedBy", "approvedByName", "rationale", "createdAt", "updatedAt"] as const;
const slaSeverities = ["critical", "high", "medium", "low", "info"] as const;

function parseReportSLAPolicy(value: unknown, workspace: string | null): ReportSLAPolicy {
  const item = exactObject(value, "remediation SLA policy", slaPolicyKeys);
  const result: ReportSLAPolicy = {
    workspaceId: text(item.workspaceId, "SLA policy workspace"),
    criticalDays: count(item.criticalDays, "critical SLA target"),
    highDays: count(item.highDays, "high SLA target"),
    mediumDays: count(item.mediumDays, "medium SLA target"),
    lowDays: count(item.lowDays, "low SLA target"),
    infoDays: count(item.infoDays, "info SLA target"),
    revision: count(item.revision, "SLA policy revision"),
    approvedBy: nullableText(item.approvedBy, "SLA policy approver"),
    approvedByName: nullableText(item.approvedByName, "SLA policy approver name"),
    rationale: text(item.rationale, "SLA policy rationale"),
    createdAt: timestamp(item.createdAt, "SLA policy creation time"),
    updatedAt: timestamp(item.updatedAt, "SLA policy update time"),
  };
  const targets = [result.criticalDays, result.highDays, result.mediumDays, result.lowDays, result.infoDays];
  if (result.workspaceId !== workspace || result.revision < 1 ||
    targets.some((target) => target < 1 || target > 3650) ||
    targets.some((target, index) => index > 0 && targets[index - 1] > target) ||
    (result.approvedBy === null) !== (result.approvedByName === null) ||
    result.approvedBy !== null && !/^[a-f0-9]{32}$/.test(result.approvedBy) ||
    result.rationale.trim() === "" || result.rationale.includes("\0") ||
    new TextEncoder().encode(result.rationale).byteLength > 8192 ||
    Date.parse(result.updatedAt) < Date.parse(result.createdAt)) {
    return invalid("remediation SLA policy");
  }
  return result;
}

function parseReportSLAPolicyEnvelope(value: unknown, workspace: string | null): ReportSLAPolicyResponse {
  const body = exactObject(value, "SLA policy response", ["apiVersion", "dataOrigin", "policy"]);
  if (body.apiVersion !== apiVersion || body.dataOrigin !== "live") return invalid("SLA policy envelope");
  return { apiVersion, dataOrigin: "live", policy: parseReportSLAPolicy(body.policy, workspace) };
}

function parseRemediationSLASummary(value: unknown, workspace: string | null): RemediationSLAResponse {
  const body = exactObject(value, "remediation SLA response", ["apiVersion", "dataOrigin", "sla"]);
  if (body.apiVersion !== apiVersion || body.dataOrigin !== "live") return invalid("remediation SLA envelope");
  const item = exactObject(body.sla, "remediation SLA summary",
    ["workspaceId", "asOf", "policy", "totals", "bySeverity", "verification"]);
  const workspaceId = text(item.workspaceId, "remediation SLA workspace");
  const asOf = timestamp(item.asOf, "remediation SLA as-of time");
  const policy = parseReportSLAPolicy(item.policy, workspace);
  const rawTotals = exactObject(item.totals, "remediation SLA totals",
    ["tracked", "withinTarget", "breached"]);
  const totals = {
    tracked: count(rawTotals.tracked, "tracked SLA findings"),
    withinTarget: count(rawTotals.withinTarget, "within-target SLA findings"),
    breached: count(rawTotals.breached, "breached SLA findings"),
  };
  const rawSeverity = exactObject(item.bySeverity, "remediation SLA severity counts", slaSeverities);
  const bySeverity = Object.fromEntries(slaSeverities.map((severity) => {
    const counts = exactObject(rawSeverity[severity], `${severity} SLA counts`, ["tracked", "breached"]);
    const tracked = count(counts.tracked, `${severity} tracked SLA findings`);
    const breached = count(counts.breached, `${severity} breached SLA findings`);
    if (breached > tracked) return invalid(`${severity} SLA counts`);
    return [severity, { tracked, breached }];
  })) as RemediationSLAResponse["sla"]["bySeverity"];
  if (workspaceId !== workspace || totals.tracked !== totals.withinTarget + totals.breached ||
    slaSeverities.reduce((sum, severity) => sum + bySeverity[severity].tracked, 0) !== totals.tracked ||
    slaSeverities.reduce((sum, severity) => sum + bySeverity[severity].breached, 0) !== totals.breached) {
    return invalid("remediation SLA arithmetic");
  }
  const verification = exactObject(item.verification, "remediation SLA verification", ["state", "reason"]);
  if (verification.state !== "not-run" || verification.reason !== remediationSLAVerificationReason) {
    return invalid("remediation SLA verification");
  }
  return {
    apiVersion, dataOrigin: "live",
    sla: {
      workspaceId, asOf, policy, totals, bySeverity,
      verification: { state: "not-run", reason: remediationSLAVerificationReason },
    },
  };
}

function slaPolicyTarget(policy: ReportSLAPolicy, severity: RemediationSLAFinding["severity"]) {
  switch (severity) {
    case "critical": return policy.criticalDays;
    case "high": return policy.highDays;
    case "medium": return policy.mediumDays;
    case "low": return policy.lowDays;
    case "info": return policy.infoDays;
  }
}

function parseRemediationSLAFinding(value: unknown, summary: RemediationSLAResponse["sla"],
  expectedStatus: RemediationSLAStatus): RemediationSLAFinding {
  const item = exactObject(value, "remediation SLA finding", [
    "findingId", "title", "assetId", "assetName", "severity", "ownerId", "ownerName",
    "workflowState", "disposition", "sourceState", "firstObservedAt", "dueAt",
    "targetDays", "status", "overdueSeconds",
  ]);
  const result: RemediationSLAFinding = {
    findingId: reportIdentifier(item.findingId, "SLA finding"),
    title: text(item.title, "SLA finding title"),
    assetId: reportIdentifier(item.assetId, "SLA finding asset"),
    assetName: text(item.assetName, "SLA finding asset name"),
    severity: choice(item.severity, slaSeverities, "SLA finding severity"),
    ownerId: nullableText(item.ownerId, "SLA finding owner"),
    ownerName: nullableText(item.ownerName, "SLA finding owner name"),
    workflowState: choice(item.workflowState,
      ["open", "in-progress", "pending-retest"], "SLA finding workflow"),
    disposition: choice(item.disposition,
      ["none", "accepted-risk", "suppressed", "false-positive"], "SLA finding disposition"),
    sourceState: choice(item.sourceState,
      ["observed", "inferred-resolved", "stale", "unknown"], "SLA finding source state"),
    firstObservedAt: timestamp(item.firstObservedAt, "SLA finding first observed time"),
    dueAt: timestamp(item.dueAt, "SLA finding due time"),
    targetDays: count(item.targetDays, "SLA finding target days"),
    status: choice(item.status, ["breached", "within-target"], "SLA finding status"),
    overdueSeconds: count(item.overdueSeconds, "SLA finding overdue seconds"),
  };
  const due = Date.parse(result.firstObservedAt) + result.targetDays * 86_400_000;
  const expectedTarget = slaPolicyTarget(summary.policy, result.severity);
  const expected = due < Date.parse(summary.asOf) ? "breached" : "within-target";
  const overdue = Math.floor(Math.max(0, Date.parse(summary.asOf) - due) / 1_000);
  if ((result.ownerId === null) !== (result.ownerName === null) ||
    result.ownerId !== null && !/^[a-f0-9]{32}$/.test(result.ownerId) ||
    result.targetDays !== expectedTarget || Date.parse(result.dueAt) !== due ||
    result.status !== expectedStatus || result.status !== expected ||
    result.overdueSeconds !== overdue) {
    return invalid("remediation SLA finding arithmetic");
  }
  return result;
}

function parseRemediationSLAFindingPage(value: unknown, summary: RemediationSLAResponse["sla"],
  status: RemediationSLAStatus, limit: number, cursor: string): RemediationSLAFindingPage {
  const body = exactObject(value, "remediation SLA finding page",
    ["apiVersion", "dataOrigin", "items", "total", "nextCursor"]);
  if (body.apiVersion !== apiVersion || body.dataOrigin !== "live") return invalid("SLA finding page envelope");
  const items = array(body.items, "SLA finding list")
    .map((item) => parseRemediationSLAFinding(item, summary, status));
  const total = count(body.total, "SLA finding total");
  const nextCursor = nullableText(body.nextCursor, "SLA finding cursor");
  const expectedTotal = status === "breached" ? summary.totals.breached : summary.totals.withinTarget;
  if (total !== expectedTotal || items.length > limit || total < items.length ||
    new Set(items.map((item) => item.findingId)).size !== items.length ||
    items.some((item, index) =>
      item.findingId <= (index === 0 ? cursor : items[index - 1].findingId))) {
    return invalid("SLA finding page order");
  }
  if (nextCursor !== null && (!/^[a-f0-9]{32}$/.test(nextCursor) ||
    items.length !== limit || nextCursor !== items.at(-1)?.findingId || nextCursor <= cursor) ||
    items.length < limit && nextCursor !== null ||
    cursor === "" && (total > items.length) !== (nextCursor !== null)) {
    return invalid("SLA finding pagination");
  }
  return { apiVersion, dataOrigin: "live", items, total, nextCursor };
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

function reportExportItem(value: unknown, workspace: string | null): ReportExport {
  const item = exactObject(value, "report export", [
    "id", "workspaceId", "snapshotId", "snapshotName", "requestedBy", "format", "state",
    "createdAt", "completedAt", "failure", "digest", "sizeBytes", "filename",
  ]);
  const workspaceId = reportIdentifier(item.workspaceId, "report export workspace");
  if (workspaceId !== workspace) return invalid("report export workspace");
  const snapshotId = reportIdentifier(item.snapshotId, "report export snapshot");
  const snapshotName = text(item.snapshotName, "report export snapshot name");
  if (snapshotName.includes("\0") || new TextEncoder().encode(snapshotName).byteLength > 256) {
    return invalid("report export snapshot name");
  }
  const format = choice(item.format, ["json", "csv"], "report export format");
  const state = choice(item.state, ["queued", "processing", "succeeded", "failed"], "report export state");
  const createdAt = timestamp(item.createdAt, "report export creation time");
  const completedAt = nullableTimestamp(item.completedAt, "report export completion time");
  let failure: ReportExport["failure"] = null;
  if (item.failure !== null) {
    const detail = exactObject(item.failure, "report export failure", ["code", "message", "retryable"]);
    const code = text(detail.code, "report export failure code");
    const message = text(detail.message, "report export failure message");
    const retryable = boolean(detail.retryable, "report export retry policy");
    if (retryable || code.includes("\0") || message.includes("\0") ||
      new TextEncoder().encode(code).byteLength > 128 ||
      new TextEncoder().encode(message).byteLength > 1024) {
      return invalid("report export failure");
    }
    failure = { code, message, retryable: false };
  }
  const digest = nullableText(item.digest, "report export digest");
  const sizeBytes = item.sizeBytes === null ? null : count(item.sizeBytes, "report export size");
  const filename = nullableText(item.filename, "report export filename");
  const expectedFilename = `aspm-report-${snapshotId}.${format}`;
  const terminal = completedAt !== null;
  const invalidState = state === "queued" || state === "processing"
    ? terminal || failure !== null || digest !== null || sizeBytes !== null || filename !== null
    : state === "failed"
      ? !terminal || failure === null || digest !== null || sizeBytes !== null || filename !== null
      : !terminal || failure !== null || digest === null || !/^sha256:[a-f0-9]{64}$/.test(digest) ||
        sizeBytes === null || sizeBytes > 256 << 10 || filename !== expectedFilename;
  if (terminal && Date.parse(completedAt) < Date.parse(createdAt) || invalidState) {
    return invalid("report export state metadata");
  }
  return {
    id: reportIdentifier(item.id, "report export identifier"), workspaceId, snapshotId, snapshotName,
    requestedBy: reportIdentifier(item.requestedBy, "report export requester"),
    format, state, createdAt, completedAt, failure, digest, sizeBytes, filename,
  };
}

function parseReportExport(value: unknown, workspace: string | null): ReportExportResponse {
  const body = exactObject(value, "report export response", ["apiVersion", "dataOrigin", "export"]);
  if (body.apiVersion !== apiVersion || body.dataOrigin !== "live") return invalid("report export response envelope");
  return { apiVersion, dataOrigin: "live", export: reportExportItem(body.export, workspace) };
}

function parseReportExports(value: unknown, workspace: string | null, limit: number, cursor: string): ReportExportsResponse {
  const body = exactObject(value, "report export page", ["apiVersion", "dataOrigin", "items", "total", "nextCursor"]);
  if (body.apiVersion !== apiVersion || body.dataOrigin !== "live") return invalid("report export page envelope");
  const items = array(body.items, "report export history").map((item) => reportExportItem(item, workspace));
  const total = count(body.total, "report export total");
  const nextCursor = body.nextCursor === null ? null : reportIdentifier(body.nextCursor, "report export cursor");
  if (new Set(items.map((item) => item.id)).size !== items.length ||
    total < items.length || items.length > limit ||
    items.some((item, index) => item.id <= (index === 0 ? cursor : items[index - 1].id)) ||
    nextCursor !== null && (items.length !== limit || nextCursor !== items.at(-1)?.id || nextCursor <= cursor) ||
    items.length < limit && nextCursor !== null ||
    cursor === "" && (total > items.length) !== (nextCursor !== null)) {
    return invalid("report export page order");
  }
  return { apiVersion, dataOrigin: "live", items, total, nextCursor };
}

function verificationBindingText(value: unknown, field: string, limit = 256): string {
  const result = text(value, field);
  if (result.includes("\0") || new TextEncoder().encode(result).byteLength > limit) return invalid(field);
  return result;
}

function verificationEvidenceItem(value: unknown, workspace: string | null,
  findingId: string): DeterministicVerificationEvidence {
  const item = exactObject(value, "verification evidence", [
    "id", "workspaceId", "findingId", "submittedBy", "method", "schema", "environmentId",
    "scopeRevision", "findingEvidenceRevision", "digest", "sizeBytes", "createdAt",
  ]);
  const itemWorkspace = reportIdentifier(item.workspaceId, "verification evidence workspace");
  const itemFinding = reportIdentifier(item.findingId, "verification evidence finding");
  const digest = text(item.digest, "verification evidence digest");
  const sizeBytes = count(item.sizeBytes, "verification evidence size");
  if (itemWorkspace !== workspace || itemFinding !== findingId ||
    !/^sha256:[a-f0-9]{64}$/.test(digest) || sizeBytes < 1 || sizeBytes > 64 << 10) {
    return invalid("verification evidence binding");
  }
  return {
    id: reportIdentifier(item.id, "verification evidence identifier"),
    workspaceId: itemWorkspace, findingId: itemFinding,
    submittedBy: reportIdentifier(item.submittedBy, "verification evidence submitter"),
    method: choice(item.method, ["deterministic-evidence"], "verification method"),
    schema: choice(item.schema, ["aspm.synthetic-fixture/v1"], "verification fixture schema"),
    environmentId: verificationBindingText(item.environmentId, "verification environment"),
    scopeRevision: verificationBindingText(item.scopeRevision, "verification scope revision"),
    findingEvidenceRevision: count(item.findingEvidenceRevision, "finding evidence revision"),
    digest, sizeBytes, createdAt: timestamp(item.createdAt, "verification evidence creation time"),
  };
}

function verificationApprovalItem(value: unknown, workspace: string | null,
  findingId: string): DeterministicVerificationApproval {
  const item = exactObject(value, "verification approval", [
    "id", "workspaceId", "findingId", "evidenceId", "approvedBy", "method", "environmentId",
    "scopeRevision", "findingEvidenceRevision", "evidenceDigest", "rationale", "createdAt",
    "expiresAt", "revokedAt", "revokedBy", "revocationRationale", "current",
  ]);
  const itemWorkspace = reportIdentifier(item.workspaceId, "verification approval workspace");
  const itemFinding = reportIdentifier(item.findingId, "verification approval finding");
  const revokedAt = nullableTimestamp(item.revokedAt, "verification revocation time");
  const revokedBy = item.revokedBy === null ? null : reportIdentifier(item.revokedBy, "verification revoker");
  const revocationRationale = nullableText(item.revocationRationale, "verification revocation rationale");
  const current = boolean(item.current, "verification approval current state");
  const evidenceDigest = text(item.evidenceDigest, "verification approval digest");
  if (itemWorkspace !== workspace || itemFinding !== findingId ||
    !/^sha256:[a-f0-9]{64}$/.test(evidenceDigest) ||
    (revokedAt === null) !== (revokedBy === null) || (revokedAt === null) !== (revocationRationale === null) ||
    current && revokedAt !== null) {
    return invalid("verification approval binding");
  }
  const rationale = verificationBindingText(item.rationale, "verification approval rationale", 8192);
  const revokedReason = revocationRationale === null ? null :
    verificationBindingText(revocationRationale, "verification revocation rationale", 8192);
  return {
    id: reportIdentifier(item.id, "verification approval identifier"),
    workspaceId: itemWorkspace, findingId: itemFinding,
    evidenceId: reportIdentifier(item.evidenceId, "verification approval evidence"),
    approvedBy: reportIdentifier(item.approvedBy, "verification approver"),
    method: choice(item.method, ["deterministic-evidence"], "verification approval method"),
    environmentId: verificationBindingText(item.environmentId, "verification approval environment"),
    scopeRevision: verificationBindingText(item.scopeRevision, "verification approval scope revision"),
    findingEvidenceRevision: count(item.findingEvidenceRevision, "approval evidence revision"),
    evidenceDigest, rationale,
    createdAt: timestamp(item.createdAt, "verification approval creation time"),
    expiresAt: timestamp(item.expiresAt, "verification approval expiry"),
    revokedAt, revokedBy, revocationRationale: revokedReason, current,
  };
}

function deterministicVerificationItem(value: unknown, workspace: string | null,
  findingId: string): DeterministicVerification {
  const item = exactObject(value, "deterministic verification", [
    "id", "workspaceId", "findingId", "approvalId", "evidenceId", "requestedBy", "method",
    "environmentId", "scopeRevision", "findingEvidenceRevision", "evidenceDigest", "state",
    "createdAt", "completedAt", "failure", "result",
  ]);
  const itemWorkspace = reportIdentifier(item.workspaceId, "verification workspace");
  const itemFinding = reportIdentifier(item.findingId, "verification finding");
  const state = choice(item.state,
    ["queued", "processing", "succeeded", "blocked", "failed", "cancelled"], "verification state");
  const completedAt = nullableTimestamp(item.completedAt, "verification completion time");
  let failure: DeterministicVerification["failure"] = null;
  if (item.failure !== null) {
    const detail = exactObject(item.failure, "verification failure", ["code", "message", "retryable"]);
    const code = verificationBindingText(detail.code, "verification failure code", 128);
    const message = verificationBindingText(detail.message, "verification failure message", 1024);
    if (boolean(detail.retryable, "verification retry policy")) return invalid("verification retry policy");
    failure = { code, message, retryable: false };
  }
  let result: DeterministicVerification["result"] = null;
  if (item.result !== null) {
    const detail = exactObject(item.result, "verification result", [
      "method", "environmentId", "scopeRevision", "evidenceId", "evidenceDigest", "outcome",
      "closeFinding", "falsePositive",
    ]);
    const closeFinding = boolean(detail.closeFinding, "verification close-finding state");
    const falsePositive = boolean(detail.falsePositive, "verification false-positive state");
    const evidenceDigest = text(detail.evidenceDigest, "verification result digest");
    if (closeFinding || falsePositive || !/^sha256:[a-f0-9]{64}$/.test(evidenceDigest)) {
      return invalid("verification safety result");
    }
    result = {
      method: choice(detail.method, ["deterministic-evidence"], "verification result method"),
      environmentId: verificationBindingText(detail.environmentId, "verification result environment"),
      scopeRevision: verificationBindingText(detail.scopeRevision, "verification result scope revision"),
      evidenceId: reportIdentifier(detail.evidenceId, "verification result evidence"),
      evidenceDigest,
      outcome: choice(detail.outcome, ["reproduced", "not-reproduced"], "verification outcome"),
      closeFinding: false, falsePositive: false,
    };
  }
  const terminal = completedAt !== null;
  const invalidState = state === "queued" || state === "processing"
    ? terminal || failure !== null || result !== null
    : state === "succeeded"
      ? !terminal || failure !== null || result === null
      : !terminal || failure === null || result !== null;
  const evidenceDigest = text(item.evidenceDigest, "verification evidence digest");
  if (itemWorkspace !== workspace || itemFinding !== findingId ||
    !/^sha256:[a-f0-9]{64}$/.test(evidenceDigest) || invalidState) {
    return invalid("verification state metadata");
  }
  const environmentId = verificationBindingText(item.environmentId, "verification environment");
  const scopeRevision = verificationBindingText(item.scopeRevision, "verification scope revision");
  const evidenceId = reportIdentifier(item.evidenceId, "verification evidence");
  if (result && (result.environmentId !== environmentId || result.scopeRevision !== scopeRevision ||
    result.evidenceId !== evidenceId || result.evidenceDigest !== evidenceDigest)) {
    return invalid("verification result binding");
  }
  return {
    id: reportIdentifier(item.id, "verification identifier"),
    workspaceId: itemWorkspace, findingId: itemFinding,
    approvalId: reportIdentifier(item.approvalId, "verification approval"),
    evidenceId, requestedBy: reportIdentifier(item.requestedBy, "verification requester"),
    method: choice(item.method, ["deterministic-evidence"], "verification method"),
    environmentId, scopeRevision,
    findingEvidenceRevision: count(item.findingEvidenceRevision, "verification evidence revision"),
    evidenceDigest, state, createdAt: timestamp(item.createdAt, "verification creation time"),
    completedAt, failure, result,
  };
}

function verificationPage<T>(value: unknown, field: string, workspace: string | null, findingId: string,
  limit: number, cursor: string, parse: (item: unknown, workspace: string | null, findingId: string) => T & { id: string }) {
  const body = exactObject(value, field, ["apiVersion", "dataOrigin", "items", "total", "nextCursor"]);
  if (body.apiVersion !== apiVersion) return invalid(`${field} API version`);
  const dataOrigin = choice(body.dataOrigin, ["synthetic", "live"], `${field} data origin`);
  const items = array(body.items, field).map((item) => parse(item, workspace, findingId));
  const total = count(body.total, `${field} total`);
  const nextCursor = body.nextCursor === null ? null : reportIdentifier(body.nextCursor, `${field} cursor`);
  if (new Set(items.map((item) => item.id)).size !== items.length || total < items.length ||
    items.length > limit || items.some((item, index) => item.id <= (index === 0 ? cursor : items[index - 1].id)) ||
    nextCursor !== null && (items.length !== limit || nextCursor !== items.at(-1)?.id || nextCursor <= cursor) ||
    items.length < limit && nextCursor !== null ||
    cursor === "" && (total > items.length) !== (nextCursor !== null)) {
    return invalid(`${field} order`);
  }
  return { apiVersion, dataOrigin, items, total, nextCursor };
}

function parseVerificationEvidenceResponse(value: unknown, workspace: string | null,
  findingId: string): VerificationEvidenceResponse {
  const body = exactObject(value, "verification evidence response", ["apiVersion", "dataOrigin", "evidence"]);
  if (body.apiVersion !== apiVersion) return invalid("verification evidence API version");
  return {
    apiVersion, dataOrigin: choice(body.dataOrigin, ["synthetic", "live"], "verification evidence origin"),
    evidence: verificationEvidenceItem(body.evidence, workspace, findingId),
  };
}

function parseVerificationApprovalResponse(value: unknown, workspace: string | null,
  findingId: string): VerificationApprovalResponse {
  const body = exactObject(value, "verification approval response", ["apiVersion", "dataOrigin", "approval"]);
  if (body.apiVersion !== apiVersion) return invalid("verification approval API version");
  return {
    apiVersion, dataOrigin: choice(body.dataOrigin, ["synthetic", "live"], "verification approval origin"),
    approval: verificationApprovalItem(body.approval, workspace, findingId),
  };
}

function parseDeterministicVerificationResponse(value: unknown, workspace: string | null,
  findingId: string): DeterministicVerificationResponse {
  const body = exactObject(value, "verification response", ["apiVersion", "dataOrigin", "verification"]);
  if (body.apiVersion !== apiVersion) return invalid("verification API version");
  return {
    apiVersion, dataOrigin: choice(body.dataOrigin, ["synthetic", "live"], "verification origin"),
    verification: deterministicVerificationItem(body.verification, workspace, findingId),
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
  const reportIntake = array(body.reportIntake, "report intake catalog").map((value): ReportIntakeSummary => {
    const item = object(value, "report intake adapter");
    const result: ReportIntakeSummary = {
      id: choice(item.id, ["sarif", "trivy", "zap", "gitleaks", "generic-json", "generic-csv", "manual"], "report intake format"),
      name: text(item.name, "report intake name"),
      kind: choice(item.kind, ["report-importer", "manual-intake"], "report intake kind"),
      implementationStatus: choice(item.implementationStatus, ["implemented"], "report intake implementation"),
      supportMaturity: choice(item.supportMaturity, ["experimental", "supported"], "report intake maturity"),
      countsAsNativeLaunchFamily: boolean(item.countsAsNativeLaunchFamily, "native family classification") as false,
      readyToImport: boolean(item.readyToImport, "report intake readiness") as true,
      supportedVersions: array(item.supportedVersions, "supported report versions").map((value) => text(value, "supported report version")),
      fieldCoverage: array(item.fieldCoverage, "report field coverage").map((value) => text(value, "report field")),
      lifecycleCapabilities: array(item.lifecycleCapabilities, "report lifecycle capabilities").map((value) => text(value, "report lifecycle capability")),
      mappingMode: choice(item.mappingMode, ["none", "declarative-fields"], "report mapping mode"),
      deterministicTestEvidenceRef: text(item.deterministicTestEvidenceRef, "report adapter fixture"),
    };
    if (result.countsAsNativeLaunchFamily || !result.readyToImport ||
      result.supportedVersions.length === 0 || result.fieldCoverage.length === 0 ||
      result.lifecycleCapabilities.length === 0) return invalid("report intake adapter");
    return result;
  });
  return { apiVersion, dataOrigin, items: uniqueIds(items), reportIntake: uniqueIds(reportIntake) };
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
  expectedStatus?: 200 | 201 | 202 | 204 | readonly (200 | 201 | 202 | 204)[];
  decodeBody?: (response: Response) => Promise<unknown>;
}

async function discardUnexpectedResponse(response: Response): Promise<void> {
  const reader = response.body?.getReader();
  if (!reader) return;
  try {
    let bytes = 0;
    // Complete small rejected replies without retaining data; cancel oversized streams.
    while (bytes <= 32 << 10) {
      const chunk = await reader.read();
      if (chunk.done) return;
      bytes += chunk.value.byteLength;
    }
    await reader.cancel();
  } catch {
    await reader.cancel().catch(() => {});
  } finally {
    reader.releaseLock();
  }
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
    if (!accepted.some((status) => status === response.status)) {
      await discardUnexpectedResponse(response);
      if (signal.aborted || (scoped && authority.revision !== requestAuthority().revision)) {
        throw new DOMException("Request scope ended", "AbortError");
      }
      return invalid("HTTP response status", response.status);
    }
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
    throw new APIError("The data service did not return JSON. Check that the API is running, then retry.", "invalid-response", true, null, response.status);
  }
  if (!response.ok) {
    try {
      const body = object(payload, "error response");
      if (body.apiVersion !== apiVersion) return invalid("error API version", response.status);
      const error = object(body.error, "error detail");
      throw new APIError(
        text(error.message, "error message"),
        choice(error.code, ["unauthorized", "forbidden", "not-found", "unavailable", "invalid-input", "conflict",
          "replay-expired", "too-large", "unsupported-format", "method-not-allowed",
          "evidence-expired", "evidence-missing", "evidence-corrupt", "history-not-archived"], "error code"),
        boolean(error.retryable, "retry policy"),
        text(error.requestId, "request identifier"),
        response.status,
      );
    } catch (cause) {
      if (cause instanceof APIError && cause.httpStatus === null) {
        throw new APIError(cause.message, cause.code, cause.retryable, cause.requestId, response.status);
      }
      throw cause;
    }
  }
  if (scoped && authority.revision !== requestAuthority().revision) throw new DOMException("Workspace changed", "AbortError");
  return parse(payload, response.status);
}

async function reportRead<T>(path: string, parse: (value: unknown) => T, signal: AbortSignal,
  headers?: Record<string, string>): Promise<T> {
  const scopedSignal = AbortSignal.any([signal, requestAuthority().signal]);
  // Allow an abandoned mounting effect to cancel before starting network I/O.
  await Promise.resolve();
  scopedSignal.throwIfAborted();
  return request(path, parse, { signal: scopedSignal, expectedStatus: 200, headers });
}

export const api = {
  work: (signal: AbortSignal, options?: { q?: string; cursor?: string; meaningfulChanges?: boolean }) => {
    const query = new URLSearchParams();
    const q = workSearchQuery(options?.q ?? "");
    if (q !== "") query.set("q", q);
    if (options?.meaningfulChanges) query.set("change", "meaningful");
    const cursor = options?.cursor;
    if (cursor !== undefined && (typeof cursor !== "string" || !/^[a-f0-9]{32}$/.test(cursor))) {
      throw new APIError("Finding pages require a native 32-character lowercase hexadecimal cursor.", "invalid-input", false);
    }
    if (cursor !== undefined) { query.set("limit", "100"); query.set("cursor", cursor); }
    const path = query.size === 0 ? "/api/v1/work" : `/api/v1/work?${query}`;
    return request(path, (value) => cursor === undefined ? parseWork(value) : validateWorkContinuation(parseWork(value), cursor),
      { signal, expectedStatus: 200 });
  },
  finding: (id: string, signal: AbortSignal, cursors?: {
    notesCursor?: string; observationsCursor?: string; decisionsCursor?: string;
  }) => {
    const workspace = requestAuthority().workspace;
    const notesCursor = findingCursor(cursors?.notesCursor), observationsCursor = findingCursor(cursors?.observationsCursor);
    const decisionsCursor = decisionHistoryCursor(cursors?.decisionsCursor);
    const query = new URLSearchParams();
    if (notesCursor !== "") query.set("notesCursor", notesCursor);
    if (observationsCursor !== "") query.set("observationsCursor", observationsCursor);
    if (decisionsCursor !== "") query.set("decisionsCursor", decisionsCursor);
    const path = `/api/v1/findings/${encodeURIComponent(id)}`;
    return request(query.size === 0 ? path : `${path}?${query}`, (value) => {
      const result = parseFinding(value);
      if (result.finding.id !== id || (result.finding.workspaceId !== undefined && result.finding.workspaceId !== workspace)) {
        return invalid("selected finding");
      }
      validateFindingHistoryPage(result.finding.notes, result.finding.notesNextCursor, notesCursor, "notes");
      validateFindingHistoryPage(result.finding.observations, result.finding.observationsNextCursor, observationsCursor, "observations");
      validateDecisionHistoryPage(result.finding.decisionEvents, result.finding.decisionEventsNextCursor, decisionsCursor);
      return result;
    }, { signal, expectedStatus: 200 });
  },
  findingHandoff: (id: string, signal: AbortSignal) => {
    reportIdentifier(id, "finding");
    return request(`/api/v1/findings/${encodeURIComponent(id)}/handoff`, (value) => {
      if (typeof value !== "string" || value.length === 0 || value.includes("\0") ||
        new TextEncoder().encode(value).byteLength > (128 << 10) ||
        !value.startsWith("ASPM DEVELOPER HANDOFF\n")) return invalid("developer handoff");
      return value;
    }, {
      signal, expectedStatus: 200, headers: { Accept: "text/plain" },
      decodeBody: (response) => response.text(),
    });
  },
  updateFinding: (id: string, input: FindingPatch, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    const body: FindingPatch = {};
    if (input.ownerId !== undefined) body.ownerId = input.ownerId;
    if (input.workflowState !== undefined) body.workflowState = input.workflowState;
    if (input.disposition !== undefined) body.disposition = input.disposition;
    if (input.acceptedRiskExpiresAt !== undefined) body.acceptedRiskExpiresAt = input.acceptedRiskExpiresAt;
    if (input.dispositionScope !== undefined) body.dispositionScope = input.dispositionScope;
    if (input.suppressionExpiresAt !== undefined) body.suppressionExpiresAt = input.suppressionExpiresAt;
    if (input.rationale !== undefined) {
      if (input.rationale.includes("\0") || new TextEncoder().encode(input.rationale).byteLength > 8192 ||
        (input.rationale !== "" && input.rationale.trim() === "")) {
        throw new APIError("A decision rationale must be NUL-free and at most 8192 UTF-8 bytes.", "invalid-input", false);
      }
      body.rationale = input.rationale;
    }
    findingBodyLimit(body, 16 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(id)}`, (value) => parseFindingUpdate(value, id, workspace),
      { method: "PATCH", body, signal, expectedStatus: 200 });
  },
  bulkUpdateFindings: (input: FindingBulkPatch, signal: AbortSignal) => {
    const ids = [...input.findingIds];
    if (ids.length === 0 || ids.length > 100 || new Set(ids).size !== ids.length) {
      throw new APIError("Select between 1 and 100 distinct findings.", "invalid-input", false);
    }
    for (const id of ids) reportIdentifier(id, "bulk finding");
    const riskMode = input.disposition !== undefined || input.decisionRevisions !== undefined ||
      Object.hasOwn(input, "acceptedRiskExpiresAt");
    if (riskMode ? input.ownerId !== undefined || input.workflowState !== undefined ||
      input.disposition !== "accepted-risk" || input.decisionRevisions === undefined ||
      !Object.hasOwn(input, "acceptedRiskExpiresAt") :
      input.ownerId === undefined && input.workflowState === undefined) {
      throw new APIError("Choose one bounded bulk triage or accepted-risk action.", "invalid-input", false);
    }
    if (input.ownerId !== undefined && input.ownerId !== null) reportIdentifier(input.ownerId, "bulk owner");
    if (input.rationale.trim() === "" || input.rationale.includes("\0") ||
      new TextEncoder().encode(input.rationale).byteLength > 8192) {
      throw new APIError("Bulk triage requires a nonblank, NUL-free rationale of at most 8192 UTF-8 bytes.", "invalid-input", false);
    }
    const body: FindingBulkPatch = { findingIds: ids, rationale: input.rationale };
    if (riskMode) {
      const revisions = input.decisionRevisions!;
      if (Object.keys(revisions).length !== ids.length || ids.some((id) =>
        !Object.hasOwn(revisions, id) || !Number.isSafeInteger(revisions[id]) || revisions[id] < 1)) {
        throw new APIError("Bulk accepted risk requires every current finding decision revision.", "invalid-input", false);
      }
      const expiry = input.acceptedRiskExpiresAt as string | null;
      if (expiry !== null && (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(expiry) ||
        !Number.isFinite(Date.parse(expiry)) || Date.parse(expiry) <= Date.now())) {
        throw new APIError("Bulk accepted-risk expiry must be null or a future RFC3339 timestamp.", "invalid-input", false);
      }
      body.decisionRevisions = revisions;
      body.disposition = "accepted-risk";
      body.acceptedRiskExpiresAt = expiry;
    } else {
      if (input.ownerId !== undefined) body.ownerId = input.ownerId;
      if (input.workflowState !== undefined) body.workflowState = input.workflowState;
    }
    findingBodyLimit(body, 64 << 10);
    return request("/api/v1/findings", (value) => parseBulkFindingUpdate(value, ids),
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
  previewFindingMerge: (id: string, otherFindingId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "primary finding");
    reportIdentifier(otherFindingId, "other finding");
    if (id === otherFindingId) throw new APIError("Choose a different finding to correlate.", "invalid-input", false);
    const body = { otherFindingId };
    findingBodyLimit(body, 16 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(id)}/merge-previews`,
      (value) => parseMergePreview(value, workspace, id, otherFindingId),
      { method: "POST", body, signal, expectedStatus: 200 });
  },
  correlationCandidates: (id: string, options: { limit?: number; cursor?: string } | undefined,
    signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "primary finding");
    const limit = options?.limit ?? 100;
    const cursor = options?.cursor ?? "";
    if (!Number.isInteger(limit) || limit < 1 || limit > 100 ||
      (cursor !== "" && !/^[a-f0-9]{32}$/.test(cursor))) {
      throw new APIError("Correlation candidate pages require a limit from 1 to 100 and a valid native cursor.", "invalid-input", false);
    }
    const query = new URLSearchParams({ limit: String(limit) });
    if (cursor !== "") query.set("cursor", cursor);
    return request(`/api/v1/findings/${encodeURIComponent(id)}/correlation-candidates?${query}`,
      (value) => parseCorrelationCandidates(value, workspace, cursor, limit),
      { signal, expectedStatus: 200 });
  },
  mergeFindings: (id: string, input: FindingMergeInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "primary finding");
    reportIdentifier(input.otherFindingId, "other finding");
    if (input.correlationRevision < 0 || input.primaryDecisionRevision < 1 || input.primaryEvidenceRevision < 1 ||
      input.otherDecisionRevision < 1 || input.otherEvidenceRevision < 1 ||
      input.rationale.trim() === "" || input.rationale.includes("\0") ||
      new TextEncoder().encode(input.rationale).byteLength > 8192 ||
      input.idempotencyKey.trim() === "" || input.idempotencyKey.includes("\0") ||
      new TextEncoder().encode(input.idempotencyKey).byteLength > 256) {
      throw new APIError("Merge confirmation requires current revisions, a bounded rationale and an intent key.", "invalid-input", false);
    }
    findingBodyLimit(input, 32 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(id)}/merges`,
      (value) => parseCorrelationResponse(value, workspace),
      { method: "POST", body: input, signal, expectedStatus: [200, 201] });
  },
  previewFindingSplit: (id: string, memberFindingId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "primary finding");
    reportIdentifier(memberFindingId, "member finding");
    if (id === memberFindingId) throw new APIError("Choose a secondary finding to split.", "invalid-input", false);
    const body = { memberFindingId };
    findingBodyLimit(body, 16 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(id)}/split-previews`,
      (value) => parseSplitPreview(value, workspace, id, memberFindingId),
      { method: "POST", body, signal, expectedStatus: 200 });
  },
  splitFinding: (id: string, input: FindingSplitInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "primary finding");
    reportIdentifier(input.memberFindingId, "member finding");
    if (input.correlationRevision < 1 || input.primaryDecisionRevision < 1 ||
      input.primaryEvidenceRevision < 1 || input.memberDecisionRevision < 1 ||
      input.memberEvidenceRevision < 1 || input.rationale.trim() === "" ||
      input.rationale.includes("\0") || new TextEncoder().encode(input.rationale).byteLength > 8192 ||
      input.idempotencyKey.trim() === "" || input.idempotencyKey.includes("\0") ||
      new TextEncoder().encode(input.idempotencyKey).byteLength > 256) {
      throw new APIError("Split confirmation requires current revisions, a bounded rationale and an intent key.", "invalid-input", false);
    }
    findingBodyLimit(input, 32 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(id)}/splits`,
      (value) => parseCorrelationResponse(value, workspace),
      { method: "POST", body: input, signal, expectedStatus: [200, 201] });
  },
  retentionPolicy: (signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request("/api/v1/retention/policy", (value) => parseRetentionPolicy(value, workspace),
      { signal, expectedStatus: 200 });
  },
  updateRetentionPolicy: (input: RetentionPolicyInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    if (input.revision < 1 || input.hotHistoryDays < 1 ||
      input.hotHistoryDays >= input.rawReportDays ||
      input.rawReportDays >= input.archivedEvidenceDays ||
      input.archivedEvidenceDays >= input.auditDays || input.auditDays > 3650) {
      throw new APIError("Retention days must be positive and ordered hot, raw, archive, then audit.", "invalid-input", false);
    }
    findingBodyLimit(input, 16 << 10);
    return request("/api/v1/retention/policy", (value) => parseRetentionPolicy(value, workspace),
      { method: "PATCH", body: input, signal, expectedStatus: 200 });
  },
  retentionHolds: (signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request("/api/v1/retention/holds", (value) => parseRetentionHolds(value, workspace),
      { signal, expectedStatus: 200 });
  },
  createRetentionHold: (input: { resourceKind: RetentionHold["resourceKind"]; resourceId: string; reason: string },
    signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(input.resourceId, "retention resource");
    if (input.reason.trim() === "" || input.reason.includes("\0") ||
      new TextEncoder().encode(input.reason).byteLength > 8192) {
      throw new APIError("A retention hold requires a bounded nonblank reason.", "invalid-input", false);
    }
    findingBodyLimit(input, 16 << 10);
    return request("/api/v1/retention/holds", (value) => parseRetentionHold(value, workspace),
      { method: "POST", body: input, signal, expectedStatus: 201 });
  },
  releaseRetentionHold: (id: string, revision: number, rationale: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "retention hold");
    if (revision < 1 || rationale.trim() === "" || rationale.includes("\0") ||
      new TextEncoder().encode(rationale).byteLength > 8192) {
      throw new APIError("Releasing a hold requires its current revision and a bounded rationale.", "invalid-input", false);
    }
    const body = { revision, rationale };
    findingBodyLimit(body, 16 << 10);
    return request(`/api/v1/retention/holds/${encodeURIComponent(id)}/releases`,
      (value) => parseRetentionHold(value, workspace),
      { method: "POST", body, signal, expectedStatus: 200 });
  },
  previewRetention: (signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request("/api/v1/retention/previews", (value) => parseRetentionPreview(value, workspace),
      { method: "POST", body: {}, signal, expectedStatus: 201 });
  },
  retentionPreview: (id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "retention preview");
    return request(`/api/v1/retention/previews/${encodeURIComponent(id)}`,
      (value) => parseRetentionPreview(value, workspace), { signal, expectedStatus: 200 });
  },
  approveRetentionPreview: (id: string, input: {
    revision: number; snapshotDigest: string; rationale: string; idempotencyKey: string;
  }, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "retention preview");
    if (input.revision < 1 || !/^sha256:[a-f0-9]{64}$/.test(input.snapshotDigest) ||
      input.rationale.trim() === "" || input.rationale.includes("\0") ||
      new TextEncoder().encode(input.rationale).byteLength > 8192 ||
      input.idempotencyKey.trim() === "" || input.idempotencyKey.includes("\0") ||
      new TextEncoder().encode(input.idempotencyKey).byteLength > 256) {
      throw new APIError("Approval requires the exact preview revision, digest, rationale and intent key.", "invalid-input", false);
    }
    findingBodyLimit(input, 16 << 10);
    return request(`/api/v1/retention/previews/${encodeURIComponent(id)}/approvals`,
      (value) => parseRetentionPreview(value, workspace),
      { method: "POST", body: input, signal, expectedStatus: [200, 201] });
  },
  executeRetentionPreview: (id: string, input: {
    revision: number; snapshotDigest: string; rationale: string; idempotencyKey: string;
  }, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "retention preview");
    if (input.revision < 1 || !/^sha256:[a-f0-9]{64}$/.test(input.snapshotDigest) ||
      input.rationale.trim() === "" || input.rationale.includes("\0") ||
      new TextEncoder().encode(input.rationale).byteLength > 8192 ||
      input.idempotencyKey.trim() === "" || input.idempotencyKey.includes("\0") ||
      new TextEncoder().encode(input.idempotencyKey).byteLength > 256) {
      throw new APIError("Execution requires the exact approved preview, rationale and intent key.", "invalid-input", false);
    }
    findingBodyLimit(input, 16 << 10);
    return request(`/api/v1/retention/previews/${encodeURIComponent(id)}/executions`,
      (value) => parseRetentionRun(value, workspace),
      { method: "POST", body: input, signal, expectedStatus: [200, 202] });
  },
  retentionRun: (id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "retention run");
    return request(`/api/v1/retention/runs/${encodeURIComponent(id)}`,
      (value) => parseRetentionRun(value, workspace), { signal, expectedStatus: 200 });
  },
  retentionHistory: (kind: HistoryRetentionResourceKind, id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "history resource");
    return request(`/api/v1/retention/history/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`,
      (value) => archivedHistory(value, workspace, kind, id), {
        signal, expectedStatus: 200,
        decodeBody: async (response) => {
          const bytes = new Uint8Array(await response.arrayBuffer());
          let body: string;
          try {
            body = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
          } catch {
            return invalid("archived history JSON", response.status);
          }
          const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
          const actualDigest = `sha256:${Array.from(hash, (value) => value.toString(16).padStart(2, "0")).join("")}`;
          const headers = response.headers;
          const resourceKind = headers.get("X-ASPM-History-Resource-Kind");
          const resourceId = headers.get("X-ASPM-History-Resource-ID");
          const availability = headers.get("X-ASPM-History-Availability");
          const digest = headers.get("X-ASPM-Archive-Digest");
          const size = headers.get("X-ASPM-Archive-Size");
          const revision = headers.get("X-ASPM-Detail-Revision");
          const disposition = headers.get("Content-Disposition");
          const length = headers.get("Content-Length");
          const contentType = headers.get("Content-Type");
          if (contentType !== "application/json") return invalid("archived history JSON", response.status);
          return {
            resourceKind, resourceId, availability, digest,
            sizeBytes: Number(size), detailRevision: Number(revision),
            filename: disposition?.match(/^attachment; filename="([^"]+)"$/)?.[1],
            text: body, actualDigest, contentLength: Number(length),
          };
        },
      });
  },
  observationEvidence: (id: string, signal: AbortSignal) => {
    reportIdentifier(id, "observation");
    return request(`/api/v1/observations/${encodeURIComponent(id)}/evidence`,
      (value) => {
        if (!(value instanceof ArrayBuffer)) return invalid("observation evidence bytes");
        return value;
      }, {
        signal, expectedStatus: 200, headers: { Accept: "application/json" },
        decodeBody: (response) => response.arrayBuffer(),
      });
  },
  restoreObservation: (id: string, input: { rationale: string; idempotencyKey: string },
    signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "observation");
    if (input.rationale.trim() === "" || input.rationale.includes("\0") ||
      new TextEncoder().encode(input.rationale).byteLength > 8192 ||
      input.idempotencyKey.trim() === "" || input.idempotencyKey.includes("\0") ||
      new TextEncoder().encode(input.idempotencyKey).byteLength > 256) {
      throw new APIError("Restoration requires a bounded rationale and intent key.", "invalid-input", false);
    }
    findingBodyLimit(input, 16 << 10);
    return request(`/api/v1/observations/${encodeURIComponent(id)}/restorations`,
      (value) => parseRetentionRun(value, workspace),
      { method: "POST", body: input, signal, expectedStatus: [200, 202] });
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
  importReport: (body: ImportInput, signal: AbortSignal) => request("/api/v1/imports", parseImportReceipt, { method: "POST", body, signal }),
  importStatus: (id: string, signal: AbortSignal) => request(`/api/v1/imports/${encodeURIComponent(id)}`, (value) => {
    const result = parseImportReceipt(value);
    if (result.id !== id) return invalid("import identifier");
    return result;
  }, { signal }),
  reportOverview: (days: number, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return reportRead(`/api/v1/reports/overview?freshnessDays=${days}`,
      (value) => parseReportOverview(value, workspace, days), signal);
  },
  reportTrends: (days: number, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    if (!Number.isInteger(days) || days < 1 || days > 365) {
      throw new APIError("Historical trend days must be an integer from 1 through 365.", "invalid-input", false);
    }
    return reportRead(`/api/v1/reports/trends?days=${days}`,
      (value) => parseHistoricalTrend(value, workspace, days), signal);
  },
  reportCoverageAssets: (state: CoverageAssetState, days: number, limit: number,
    cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    if (!["scanned", "unscanned", "stale", "unknown-freshness"].includes(state) ||
      !Number.isInteger(days) || days < 1 || days > 365 ||
      !Number.isInteger(limit) || limit < 1 || limit > 100 ||
      cursor !== null && !/^[a-f0-9]{32}$/.test(cursor)) {
      throw new APIError("Coverage pages require one Live coverage state, freshness from 1 through 365 days, and a bounded native cursor.", "invalid-input", false);
    }
    const query = new URLSearchParams({
      state, freshnessDays: String(days), limit: String(limit),
    });
    if (cursor !== null) query.set("cursor", cursor);
    return reportRead(`/api/v1/reports/coverage-assets?${query}`,
      (value) => parseCoverageAssetDrilldown(value, workspace, state, days, limit, cursor ?? ""), signal);
  },
  reportFindingMetrics: (metric: FindingMetric, limit: number, cursor: string | null,
    report: PostureReport, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    if (![
      "findings", "open-findings", "accepted-risk", "expired-accepted-risk",
      "suppressed", "expired-suppression", "false-positive", "inferred-resolved",
      "critical", "high", "medium", "low", "info",
    ].includes(metric) || !Number.isInteger(limit) || limit < 1 || limit > 100 ||
      cursor !== null && !/^[a-f0-9]{32}$/.test(cursor)) {
      throw new APIError("Finding metric pages require one declared metric and a bounded native cursor.",
        "invalid-input", false);
    }
    const query = new URLSearchParams({ metric, limit: String(limit) });
    if (cursor !== null) query.set("cursor", cursor);
    return reportRead(`/api/v1/reports/finding-metrics?${query}`,
      (value) => parseFindingMetric(value, workspace, metric, limit, cursor ?? "", report), signal, {
        "X-ASPM-Report-As-Of": report.asOf,
      });
  },
  reportSLA: (signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return reportRead("/api/v1/reports/sla",
      (value) => parseRemediationSLASummary(value, workspace), signal);
  },
  reportSLAFindings: (status: RemediationSLAStatus, limit: number, cursor: string | null,
    summary: RemediationSLAResponse["sla"], signal: AbortSignal) => {
    if (!["breached", "within-target"].includes(status) ||
      !Number.isInteger(limit) || limit < 1 || limit > 100 ||
      cursor !== null && !/^[a-f0-9]{32}$/.test(cursor)) {
      throw new APIError("SLA finding pages require breached or within-target status and one bounded native cursor.", "invalid-input", false);
    }
    const query = new URLSearchParams({ status, limit: String(limit) });
    if (cursor !== null) query.set("cursor", cursor);
    return reportRead(`/api/v1/reports/sla-findings?${query}`,
      (value) => parseRemediationSLAFindingPage(value, summary, status, limit, cursor ?? ""), signal, {
        "X-ASPM-SLA-As-Of": summary.asOf,
        "X-ASPM-SLA-Policy-Revision": String(summary.policy.revision),
      });
  },
  updateReportSLAPolicy: (current: ReportSLAPolicy, input: ReportSLAPolicyInput,
    signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    const targets = [input.criticalDays, input.highDays, input.mediumDays, input.lowDays, input.infoDays];
    if (input.revision !== current.revision || !Number.isSafeInteger(input.revision) || input.revision < 1 ||
      targets.some((target) => !Number.isSafeInteger(target) || target < 1 || target > 3650) ||
      targets.some((target, index) => index > 0 && targets[index - 1] > target) ||
      input.rationale.trim() === "" || input.rationale.includes("\0") ||
      new TextEncoder().encode(input.rationale).byteLength > 8192) {
      throw new APIError("SLA targets require the current revision, five ordered day values, and a bounded rationale.", "invalid-input", false);
    }
    return request("/api/v1/reports/sla-policy", (value) => {
      const response = parseReportSLAPolicyEnvelope(value, workspace);
      const policy = response.policy;
      if (policy.revision !== current.revision + 1 ||
        policy.createdAt !== current.createdAt || Date.parse(policy.updatedAt) < Date.parse(current.updatedAt) ||
        policy.approvedBy === null ||
        policy.criticalDays !== input.criticalDays || policy.highDays !== input.highDays ||
        policy.mediumDays !== input.mediumDays || policy.lowDays !== input.lowDays ||
        policy.infoDays !== input.infoDays || policy.rationale !== input.rationale) {
        return invalid("SLA policy acknowledgement");
      }
      return response;
    }, { method: "PATCH", body: input, signal, expectedStatus: 200 });
  },
  reportExports: (limit: number, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100 ||
      cursor !== null && !/^[a-f0-9]{32}$/.test(cursor)) {
      throw new APIError("Report export pages require a limit from 1 through 100 and a native cursor.", "invalid-input", false);
    }
    const query = new URLSearchParams({ limit: String(limit) });
    if (cursor !== null) query.set("cursor", cursor);
    return reportRead(`/api/v1/reports/exports?${query}`,
      (value) => parseReportExports(value, workspace, limit, cursor ?? ""), signal);
  },
  reportExport: (id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(id, "report export");
    return reportRead(`/api/v1/reports/exports/${encodeURIComponent(id)}`, (value) => {
      const response = parseReportExport(value, workspace);
      if (response.export.id !== id) return invalid("selected report export identifier");
      return response;
    }, signal);
  },
  createReportExport: (input: ReportExportInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(input.snapshotId, "report export snapshot");
    if (!["json", "csv"].includes(input.format) || input.idempotencyKey.trim() === "" ||
      input.idempotencyKey.includes("\0") ||
      new TextEncoder().encode(input.idempotencyKey).byteLength > 256) {
      throw new APIError("Report exports require a succeeded snapshot, JSON or CSV, and a bounded intent key.", "invalid-input", false);
    }
    const body: ReportExportInput = {
      snapshotId: input.snapshotId, format: input.format, idempotencyKey: input.idempotencyKey,
    };
    return request("/api/v1/reports/exports", (value, status) => {
      const response = parseReportExport(value, workspace);
      if (response.export.snapshotId !== body.snapshotId || response.export.format !== body.format ||
        status === 202 && (response.export.state === "succeeded" || response.export.state === "failed") ||
        status === 200 && response.export.state !== "succeeded" && response.export.state !== "failed") {
        return invalid("report export acknowledgement", status);
      }
      return response;
    }, { method: "POST", body, signal, expectedStatus: [200, 202] });
  },
  reportExportContent: (item: ReportExport, signal: AbortSignal): Promise<ReportExportContent> => {
    reportIdentifier(item.id, "report export");
    if (item.state !== "succeeded" || item.digest === null || item.sizeBytes === null || item.filename === null) {
      throw new APIError("This report export does not have a completed artifact.", "conflict", false);
    }
    const expectedDigest = item.digest;
    const expectedSize = item.sizeBytes;
    const expectedFilename = item.filename;
    const contentType = item.format === "json" ?
      "application/json; charset=utf-8" as const : "text/csv; charset=utf-8" as const;
    return request(`/api/v1/reports/exports/${encodeURIComponent(item.id)}/content`,
      (value) => value as ReportExportContent, {
        signal, expectedStatus: 200, headers: { Accept: contentType },
        decodeBody: async (response) => {
          const disposition = response.headers.get("Content-Disposition");
          const digest = response.headers.get("X-ASPM-Content-Digest");
          const length = response.headers.get("X-ASPM-Content-Length");
          if (response.headers.get("Content-Type") !== contentType ||
            disposition !== `attachment; filename="${expectedFilename}"` ||
            digest !== expectedDigest || length !== String(expectedSize)) {
            await discardUnexpectedResponse(response);
            return invalid("report export content headers", response.status);
          }
          const bytes = await response.arrayBuffer();
          if (bytes.byteLength !== expectedSize) return invalid("report export content length", response.status);
          const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
          const actual = `sha256:${Array.from(hash, (value) => value.toString(16).padStart(2, "0")).join("")}`;
          if (actual !== expectedDigest) return invalid("report export content integrity", response.status);
          return { bytes, contentType, filename: expectedFilename } satisfies ReportExportContent;
        },
      });
  },
  verificationEvidence: (findingId: string, limit: number, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(findingId, "verification finding");
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100 ||
      cursor !== null && !/^[a-f0-9]{32}$/.test(cursor)) {
      throw new APIError("Verification evidence pages require a limit from 1 through 100 and a native cursor.", "invalid-input", false);
    }
    const query = new URLSearchParams({ limit: String(limit) });
    if (cursor !== null) query.set("cursor", cursor);
    return reportRead(`/api/v1/findings/${encodeURIComponent(findingId)}/verification/evidence?${query}`,
      (value) => verificationPage(value, "verification evidence page", workspace, findingId,
        limit, cursor ?? "", verificationEvidenceItem) as VerificationEvidencePage, signal);
  },
  verificationApprovals: (findingId: string, limit: number, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(findingId, "verification finding");
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100 ||
      cursor !== null && !/^[a-f0-9]{32}$/.test(cursor)) {
      throw new APIError("Verification approval pages require a limit from 1 through 100 and a native cursor.", "invalid-input", false);
    }
    const query = new URLSearchParams({ limit: String(limit) });
    if (cursor !== null) query.set("cursor", cursor);
    return reportRead(`/api/v1/findings/${encodeURIComponent(findingId)}/verification/approvals?${query}`,
      (value) => verificationPage(value, "verification approval page", workspace, findingId,
        limit, cursor ?? "", verificationApprovalItem) as VerificationApprovalPage, signal);
  },
  deterministicVerifications: (findingId: string, limit: number, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(findingId, "verification finding");
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100 ||
      cursor !== null && !/^[a-f0-9]{32}$/.test(cursor)) {
      throw new APIError("Verification history pages require a limit from 1 through 100 and a native cursor.", "invalid-input", false);
    }
    const query = new URLSearchParams({ limit: String(limit) });
    if (cursor !== null) query.set("cursor", cursor);
    return reportRead(`/api/v1/findings/${encodeURIComponent(findingId)}/verification/jobs?${query}`,
      (value) => verificationPage(value, "verification history page", workspace, findingId,
        limit, cursor ?? "", deterministicVerificationItem) as DeterministicVerificationPage, signal);
  },
  deterministicVerification: (findingId: string, id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(findingId, "verification finding");
    reportIdentifier(id, "verification");
    return reportRead(`/api/v1/findings/${encodeURIComponent(findingId)}/verification/jobs/${encodeURIComponent(id)}`,
      (value) => {
        const response = parseDeterministicVerificationResponse(value, workspace, findingId);
        if (response.verification.id !== id) return invalid("selected verification identifier");
        return response;
      }, signal);
  },
  submitVerificationEvidence: (findingId: string, input: {
    environmentId: string; scopeRevision: string; condition: boolean;
  }, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(findingId, "verification finding");
    const environmentId = verificationBindingText(input.environmentId, "verification environment");
    const scopeRevision = verificationBindingText(input.scopeRevision, "verification scope revision");
    const body = {
      method: "deterministic-evidence" as const, environmentId, scopeRevision,
      fixture: { schema: "aspm.synthetic-fixture/v1" as const, environmentId, condition: input.condition },
    };
    return request(`/api/v1/findings/${encodeURIComponent(findingId)}/verification/evidence`,
      (value) => parseVerificationEvidenceResponse(value, workspace, findingId),
      { method: "POST", body, signal, expectedStatus: 201 });
  },
  approveVerificationEvidence: (findingId: string, input: {
    evidenceId: string; rationale: string; expiresAt: string;
  }, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(findingId, "verification finding");
    reportIdentifier(input.evidenceId, "verification evidence");
    verificationBindingText(input.rationale, "verification approval rationale", 8192);
    timestamp(input.expiresAt, "verification approval expiry");
    const body = { evidenceId: input.evidenceId, rationale: input.rationale, expiresAt: input.expiresAt };
    return request(`/api/v1/findings/${encodeURIComponent(findingId)}/verification/approvals`,
      (value) => parseVerificationApprovalResponse(value, workspace, findingId),
      { method: "POST", body, signal, expectedStatus: 201 });
  },
  revokeVerificationApproval: (findingId: string, approvalId: string, rationale: string,
    signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(findingId, "verification finding");
    reportIdentifier(approvalId, "verification approval");
    verificationBindingText(rationale, "verification revocation rationale", 8192);
    return request(`/api/v1/findings/${encodeURIComponent(findingId)}/verification/approvals/${encodeURIComponent(approvalId)}/revoke`,
      (value) => parseVerificationApprovalResponse(value, workspace, findingId),
      { method: "POST", body: { rationale }, signal, expectedStatus: 200 });
  },
  queueDeterministicVerification: (findingId: string, input: {
    approvalId: string; idempotencyKey: string;
  }, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    reportIdentifier(findingId, "verification finding");
    reportIdentifier(input.approvalId, "verification approval");
    verificationBindingText(input.idempotencyKey, "verification intent key", 256);
    const body = { approvalId: input.approvalId, idempotencyKey: input.idempotencyKey };
    return request(`/api/v1/findings/${encodeURIComponent(findingId)}/verification/jobs`,
      (value, status) => {
        const response = parseDeterministicVerificationResponse(value, workspace, findingId);
        if (response.verification.approvalId !== input.approvalId ||
          status === 202 && response.verification.state !== "queued") {
          return invalid("verification acknowledgement", status);
        }
        return response;
      }, { method: "POST", body, signal, expectedStatus: [200, 202] });
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
