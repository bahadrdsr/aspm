import { apiVersion } from "./api-contract";

export type ReportRole = "admin" | "analyst" | "viewer";
export type SnapshotState = "queued" | "processing" | "succeeded" | "failed";
export interface SyntheticPostureReport {
  workspaceId: string;
  asOf: string;
  totals: {
    assets: number; findings: number; openFindings: number; acceptedRisk: number;
    expiredAcceptedRisk: number; suppressed: number; expiredSuppression: number;
    falsePositive: number; inferredResolved: number; verifiedResolved: number;
  };
  bySeverity: { critical: number; high: number; medium: number; low: number; info: number };
  coverage: {
    scannedAssets: number; unscannedAssets: number; staleAssets: number;
    unknownFreshnessAssets: number; freshnessWindowDays: number;
  };
  freshnessWindow: { from: string; to: string; days: number };
  verification: { state: "not-run"; reason: string };
}
export interface SyntheticSnapshotSummary {
  id: string;
  workspaceId: string;
  name: string;
  requestedBy: string;
  freshnessDays: number;
  state: SnapshotState;
  createdAt: string;
  completedAt: string | null;
  failure: { code: string; message: string; retryable: boolean } | null;
}
export interface SyntheticSnapshot extends SyntheticSnapshotSummary {
  report: SyntheticPostureReport | null;
}
export interface SnapshotPage {
  apiVersion: typeof apiVersion;
  dataOrigin: "synthetic";
  items: SyntheticSnapshotSummary[];
  total: number;
  nextCursor: string | null;
}
export interface SyntheticTrendPoint {
  snapshotId: string;
  name: string;
  completedAt: string;
  asOf: string;
  totals: SyntheticPostureReport["totals"];
  bySeverity: SyntheticPostureReport["bySeverity"];
  coverage: SyntheticPostureReport["coverage"];
}
export interface SyntheticTrendDelta {
  findings: number; openFindings: number; acceptedRisk: number; suppressed: number; falsePositive: number;
  critical: number; high: number; medium: number; low: number; info: number;
  scannedAssets: number; unscannedAssets: number; staleAssets: number; unknownFreshnessAssets: number;
}
export interface SyntheticTrend {
  workspaceId: string;
  from: string;
  to: string;
  days: number;
  points: SyntheticTrendPoint[];
  delta: SyntheticTrendDelta | null;
  verification: { state: "not-run"; reason: string };
}
export interface SyntheticTrendResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: "live";
  trend: SyntheticTrend;
}
export type CoverageState = "scanned" | "unscanned" | "stale" | "unknown-freshness";
export interface SyntheticCoverageAsset {
  id: string;
  workspaceId: string;
  name: string;
  kind: string;
  environment: string;
  criticality: "low" | "medium" | "high" | "critical";
  tags: string[];
  ownerId: string | null;
}
export interface SyntheticCoverageItem {
  asset: SyntheticCoverageAsset;
  coverage: {
    scanned: boolean;
    stale: boolean;
    unknownFreshness: boolean;
    latestSourceScanAt: string | null;
  };
}
export interface SyntheticCoverageDrilldown {
  workspaceId: string;
  state: CoverageState;
  freshnessWindow: { from: string; to: string; days: number };
  items: SyntheticCoverageItem[];
  total: number;
  nextCursor: string | null;
  verification: { state: "not-run"; reason: string };
}
export interface SyntheticCoverageResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: "live";
  drilldown: SyntheticCoverageDrilldown;
}
export interface SyntheticSLAPolicy {
  workspaceId: string;
  criticalDays: number;
  highDays: number;
  mediumDays: number;
  lowDays: number;
  infoDays: number;
  revision: number;
  approvedBy: string | null;
  approvedByName: string | null;
  rationale: string;
  createdAt: string;
  updatedAt: string;
}
export interface SyntheticSLASummary {
  workspaceId: string;
  asOf: string;
  policy: SyntheticSLAPolicy;
  totals: { tracked: number; withinTarget: number; breached: number };
  bySeverity: Record<"critical" | "high" | "medium" | "low" | "info",
    { tracked: number; breached: number }>;
  verification: { state: "not-run"; reason: string };
}
export type SLAStatus = "breached" | "within-target";
export interface SyntheticSLAFinding {
  findingId: string;
  title: string;
  assetId: string;
  assetName: string;
  severity: "critical" | "high" | "medium" | "low" | "info";
  ownerId: string | null;
  ownerName: string | null;
  workflowState: "open" | "in-progress" | "pending-retest";
  disposition: "none" | "accepted-risk" | "suppressed" | "false-positive";
  sourceState: "observed" | "inferred-resolved" | "stale" | "unknown";
  firstObservedAt: string;
  dueAt: string;
  targetDays: number;
  status: SLAStatus;
  overdueSeconds: number;
}
export interface SyntheticSLAFindingPage {
  apiVersion: typeof apiVersion;
  dataOrigin: "live";
  items: SyntheticSLAFinding[];
  total: number;
  nextCursor: string | null;
}
export interface SyntheticSLAResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: "live";
  sla: SyntheticSLASummary;
}
export interface SyntheticSLAPolicyResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: "live";
  policy: SyntheticSLAPolicy;
}

export const reportAlpha = { id: "1".repeat(32), name: "Synthetic Reports Alpha workspace", role: "admin" as ReportRole };
export const reportBeta = { id: "3".repeat(32), name: "Synthetic Reports Beta workspace", role: "analyst" as ReportRole };
export const reportUser = { id: "a".repeat(32), name: "Synthetic reporting analyst", email: "reports@synthetic.invalid" };
export const verificationReason = "Independent verified-resolution results are not integrated with canonical findings.";
export const trendVerificationReason = "Historical snapshot trends are not an SLA, forecast, or independent verification.";
export const coverageVerificationReason = "Coverage drill-down reflects successful complete full-scan intake; it is not verification of asset safety.";
export const slaVerificationReason = "Remediation SLA status is a time-to-workflow target; it does not verify safety, resolution, or risk acceptance.";
export const overviewPath = "/api/v1/reports/overview";
export const coverageAssetsPath = "/api/v1/reports/coverage-assets";
export const snapshotsPath = "/api/v1/reports/snapshots";
export const trendsPath = "/api/v1/reports/trends";
export const slaPolicyPath = "/api/v1/reports/sla-policy";
export const slaPath = "/api/v1/reports/sla";
export const slaFindingsPath = "/api/v1/reports/sla-findings";

export const alphaSLAPolicy: SyntheticSLAPolicy = {
  workspaceId: reportAlpha.id,
  criticalDays: 7, highDays: 30, mediumDays: 90, lowDays: 180, infoDays: 365,
  revision: 4, approvedBy: reportUser.id, approvedByName: reportUser.name,
  rationale: "Approved synthetic remediation targets after explicit review.",
  createdAt: "2026-08-01T08:00:00.000Z", updatedAt: "2026-10-01T09:10:11.000Z",
};
export const betaSLAPolicy: SyntheticSLAPolicy = {
  ...structuredClone(alphaSLAPolicy), workspaceId: reportBeta.id, revision: 2,
  rationale: "Approved Beta synthetic remediation targets.",
  createdAt: "2026-08-02T08:00:00.000Z", updatedAt: "2026-09-29T10:11:12.000Z",
};

export function withFreshness(report: SyntheticPostureReport, days: number): SyntheticPostureReport {
  if (!Number.isInteger(days) || days < 1 || days > 365) throw new Error("Invalid synthetic freshness window.");
  return {
    ...structuredClone(report),
    coverage: { ...report.coverage, freshnessWindowDays: days },
    freshnessWindow: { from: new Date(Date.parse(report.asOf) - days * 86_400_000).toISOString(), to: report.asOf, days },
  };
}

const initial: SyntheticPostureReport = {
  workspaceId: reportAlpha.id,
  asOf: "2026-09-12T12:34:56.000Z",
  totals: { assets: 127, findings: 263, openFindings: 181, acceptedRisk: 37, expiredAcceptedRisk: 11,
    suppressed: 18, expiredSuppression: 4, falsePositive: 9, inferredResolved: 29, verifiedResolved: 0 },
  bySeverity: { critical: 17, high: 43, medium: 89, low: 101, info: 13 },
  coverage: { scannedAssets: 91, unscannedAssets: 36, staleAssets: 23, unknownFreshnessAssets: 7, freshnessWindowDays: 7 },
  freshnessWindow: { from: "", to: "", days: 7 },
  verification: { state: "not-run", reason: verificationReason },
};

export const alphaOverview = withFreshness(initial, 7);
export const betaOverview = withFreshness({
  ...initial, workspaceId: reportBeta.id, asOf: "2026-09-13T04:05:06.000Z",
  totals: { assets: 908, findings: 1493, openFindings: 1021, acceptedRisk: 109, expiredAcceptedRisk: 19,
    suppressed: 83, expiredSuppression: 11, falsePositive: 42, inferredResolved: 137, verifiedResolved: 0 },
  bySeverity: { critical: 83, high: 149, medium: 227, low: 401, info: 633 },
  coverage: { scannedAssets: 821, unscannedAssets: 87, staleAssets: 217, unknownFreshnessAssets: 53, freshnessWindowDays: 7 },
}, 7);
export const refreshedOverview = withFreshness({
  ...initial, asOf: "2026-09-13T13:35:57.000Z",
  totals: { assets: 211, findings: 419, openFindings: 277, acceptedRisk: 61, expiredAcceptedRisk: 19,
    suppressed: 31, expiredSuppression: 7, falsePositive: 15, inferredResolved: 47, verifiedResolved: 0 },
  bySeverity: { critical: 31, high: 67, medium: 97, low: 113, info: 111 },
  coverage: { scannedAssets: 173, unscannedAssets: 38, staleAssets: 41, unknownFreshnessAssets: 13, freshnessWindowDays: 7 },
}, 7);
export const savedReport = withFreshness({
  ...initial, asOf: "2026-08-26T05:06:07.000Z",
  totals: { assets: 71, findings: 149, openFindings: 103, acceptedRisk: 23, expiredAcceptedRisk: 5,
    suppressed: 12, expiredSuppression: 2, falsePositive: 6, inferredResolved: 17, verifiedResolved: 0 },
  bySeverity: { critical: 11, high: 19, medium: 31, low: 41, info: 47 },
  coverage: { scannedAssets: 61, unscannedAssets: 10, staleAssets: 13, unknownFreshnessAssets: 3, freshnessWindowDays: 7 },
}, 7);

const trendMetrics = [
  {
    totals: { assets: 10, findings: 10, openFindings: 8, acceptedRisk: 1, expiredAcceptedRisk: 0,
      suppressed: 2, expiredSuppression: 0, falsePositive: 0, inferredResolved: 1, verifiedResolved: 0 },
    bySeverity: { critical: 1, high: 2, medium: 3, low: 3, info: 1 },
    coverage: { scannedAssets: 8, unscannedAssets: 2, staleAssets: 3, unknownFreshnessAssets: 1, freshnessWindowDays: 7 },
  },
  {
    totals: { assets: 12, findings: 12, openFindings: 9, acceptedRisk: 2, expiredAcceptedRisk: 0,
      suppressed: 1, expiredSuppression: 0, falsePositive: 1, inferredResolved: 0, verifiedResolved: 0 },
    bySeverity: { critical: 2, high: 3, medium: 3, low: 3, info: 1 },
    coverage: { scannedAssets: 9, unscannedAssets: 3, staleAssets: 2, unknownFreshnessAssets: 1, freshnessWindowDays: 7 },
  },
  {
    totals: { assets: 9, findings: 7, openFindings: 4, acceptedRisk: 3, expiredAcceptedRisk: 0,
      suppressed: 1, expiredSuppression: 0, falsePositive: 1, inferredResolved: 0, verifiedResolved: 0 },
    bySeverity: { critical: 2, high: 1, medium: 1, low: 2, info: 1 },
    coverage: { scannedAssets: 5, unscannedAssets: 4, staleAssets: 2, unknownFreshnessAssets: 1, freshnessWindowDays: 7 },
  },
] satisfies Array<Pick<SyntheticTrendPoint, "totals" | "bySeverity" | "coverage">>;

function trendDelta(first: SyntheticTrendPoint, last: SyntheticTrendPoint): SyntheticTrendDelta {
  return {
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
}

export function syntheticTrend(workspaceId: string, days = 30, count = 3): SyntheticTrend {
  if (![reportAlpha.id, reportBeta.id].includes(workspaceId) || !Number.isInteger(days) || days < 1 || days > 365 ||
    !Number.isInteger(count) || count < 0 || count > 3) {
    throw new Error("Only bounded synthetic historical trends may be generated.");
  }
  const to = new Date("2026-10-08T13:58:54.000Z");
  const fractions = count === 0 ? [] : count === 1 ? [0.2] : count === 2 ? [0.85, 0.1] : [0.9, 0.45, 0.05];
  const prefix = workspaceId === reportAlpha.id ? "9" : "8";
  const points = fractions.map((fraction, index): SyntheticTrendPoint => {
    const completedAt = new Date(to.getTime() - Math.max(1, Math.floor(days * 86_400_000 * fraction))).toISOString();
    const metrics = trendMetrics[count === 1 ? 1 : index === count - 1 ? 2 : index];
    return {
      snapshotId: `${prefix}${(index + 1).toString(16).padStart(31, "0")}`,
      name: `Synthetic ${workspaceId === reportAlpha.id ? "Alpha" : "Beta"} historical point ${index + 1}`,
      completedAt, asOf: completedAt,
      totals: structuredClone(metrics.totals),
      bySeverity: structuredClone(metrics.bySeverity),
      coverage: structuredClone(metrics.coverage),
    };
  });
  return {
    workspaceId,
    from: new Date(to.getTime() - days * 86_400_000).toISOString(),
    to: to.toISOString(),
    days,
    points,
    delta: points.length < 2 ? null : trendDelta(points[0], points.at(-1)!),
    verification: { state: "not-run", reason: trendVerificationReason },
  };
}

export function syntheticTrendResponse(workspaceId: string, days = 30, count = 3): SyntheticTrendResponse {
  return { apiVersion, dataOrigin: "live", trend: syntheticTrend(workspaceId, days, count) };
}

export const trendStorageCanaries = (() => {
  const trends = [
    syntheticTrend(reportAlpha.id, 30),
    syntheticTrend(reportAlpha.id, 7),
    syntheticTrend(reportBeta.id, 30),
  ];
  return [
    trendVerificationReason,
    ...trends.flatMap((trend) => [
      trend.from, trend.to,
      ...trend.points.flatMap((point) => [point.snapshotId, point.name, point.completedAt]),
    ]),
  ];
})();

export function syntheticCoverageAssets(report: SyntheticPostureReport, days: number): SyntheticCoverageItem[] {
  const current = withFreshness(report, days);
  const { scannedAssets, unscannedAssets, staleAssets, unknownFreshnessAssets } = current.coverage;
  if (![reportAlpha.id, reportBeta.id].includes(current.workspaceId) ||
    current.totals.assets !== scannedAssets + unscannedAssets ||
    staleAssets > scannedAssets || unknownFreshnessAssets > scannedAssets ||
    staleAssets < Math.max(0, unknownFreshnessAssets - (staleAssets < scannedAssets ? 1 : 0))) {
    throw new Error("Synthetic coverage membership must match one bounded Live overview.");
  }
  const prefix = current.workspaceId === reportAlpha.id ? "4" : "5";
  const workspaceName = current.workspaceId === reportAlpha.id ? "Alpha" : "Beta";
  const staleAt = new Date(Date.parse(current.freshnessWindow.from) - 3_600_000).toISOString();
  const freshAt = new Date(Date.parse(current.freshnessWindow.to) - 3_600_000).toISOString();
  const hasUnknownOnly = unknownFreshnessAssets > 0 && staleAssets < scannedAssets;
  const unknownOverlap = unknownFreshnessAssets - (hasUnknownOnly ? 1 : 0);
  const unknownOnlyIndex = hasUnknownOnly ? staleAssets : -1;
  return Array.from({ length: current.totals.assets }, (_, index): SyntheticCoverageItem => {
    const scanned = index < scannedAssets;
    const stale = scanned && index < staleAssets;
    const unknownFreshness = scanned &&
      (index < unknownOverlap || index === unknownOnlyIndex);
    const latestSourceScanAt = !scanned || unknownFreshness && !stale ? null : stale ? staleAt : freshAt;
    return {
      asset: {
        id: `${prefix}${(index + 1).toString(16).padStart(31, "0")}`,
        workspaceId: current.workspaceId,
        name: `Synthetic ${workspaceName} coverage asset ${String(index + 1).padStart(4, "0")}`,
        kind: "repository",
        environment: index % 2 === 0 ? "production" : "test",
        criticality: (["critical", "high", "medium", "low"] as const)[index % 4],
        tags: ["synthetic", `coverage-${String(index + 1).padStart(4, "0")}`],
        ownerId: index % 3 === 0 ? reportUser.id : null,
      },
      coverage: { scanned, stale, unknownFreshness, latestSourceScanAt },
    };
  });
}

export function syntheticCoverageDrilldown(report: SyntheticPostureReport, state: CoverageState, days: number,
  limit = 100, cursor = ""): SyntheticCoverageDrilldown {
  if (!["scanned", "unscanned", "stale", "unknown-freshness"].includes(state) ||
    !Number.isInteger(limit) || limit < 1 || limit > 100 ||
    cursor !== "" && !/^[a-f0-9]{32}$/.test(cursor)) {
    throw new Error("Only bounded synthetic coverage drill-down pages may be generated.");
  }
  const current = withFreshness(report, days);
  const member = (item: SyntheticCoverageItem) => state === "scanned" ? item.coverage.scanned :
    state === "unscanned" ? !item.coverage.scanned :
      state === "stale" ? item.coverage.stale : item.coverage.unknownFreshness;
  const all = syntheticCoverageAssets(current, days).filter(member);
  const remaining = all.filter((item) => item.asset.id > cursor);
  const items = remaining.slice(0, limit);
  const nextCursor = remaining.length > limit ? items.at(-1)!.asset.id : null;
  const expectedTotal = state === "scanned" ? current.coverage.scannedAssets :
    state === "unscanned" ? current.coverage.unscannedAssets :
      state === "stale" ? current.coverage.staleAssets : current.coverage.unknownFreshnessAssets;
  if (all.length !== expectedTotal) throw new Error("Synthetic coverage total diverged from the Live overview metric.");
  return {
    workspaceId: current.workspaceId,
    state,
    freshnessWindow: structuredClone(current.freshnessWindow),
    items: structuredClone(items),
    total: all.length,
    nextCursor,
    verification: { state: "not-run", reason: coverageVerificationReason },
  };
}

export function syntheticCoverageResponse(report: SyntheticPostureReport, state: CoverageState, days: number,
  limit = 100, cursor = ""): SyntheticCoverageResponse {
  return { apiVersion, dataOrigin: "live", drilldown: syntheticCoverageDrilldown(report, state, days, limit, cursor) };
}

export const coverageStorageCanaries = (() => {
  const pages = [
    syntheticCoverageDrilldown(alphaOverview, "stale", 7),
    syntheticCoverageDrilldown(alphaOverview, "unknown-freshness", 30),
    syntheticCoverageDrilldown(betaOverview, "scanned", 7),
  ];
  return [
    coverageVerificationReason,
    ...pages.flatMap((page) => [
      page.freshnessWindow.from, page.freshnessWindow.to,
      ...page.items.slice(0, 3).flatMap((item) =>
        [item.asset.id, item.asset.name, item.coverage.latestSourceScanAt ?? ""]),
    ]),
  ].filter((value) => value !== "");
})();

function slaTarget(policy: SyntheticSLAPolicy, severity: SyntheticSLAFinding["severity"]) {
  return policy[`${severity}Days` as keyof Pick<SyntheticSLAPolicy,
    "criticalDays" | "highDays" | "mediumDays" | "lowDays" | "infoDays">];
}

export function syntheticSLAFindingSet(workspaceId: string,
  policy = workspaceId === reportAlpha.id ? alphaSLAPolicy : betaSLAPolicy): SyntheticSLAFinding[] {
  if (![reportAlpha.id, reportBeta.id].includes(workspaceId) || policy.workspaceId !== workspaceId) {
    throw new Error("Synthetic SLA findings require one known workspace policy.");
  }
  const asOf = Date.parse("2026-10-08T17:01:51.000Z");
  const severities = ["critical", "high", "medium", "low", "info"] as const;
  const workflows = ["open", "in-progress", "pending-retest"] as const;
  const dispositions = ["none", "accepted-risk", "suppressed", "false-positive"] as const;
  const sourceStates = ["observed", "inferred-resolved", "stale", "unknown"] as const;
  const prefix = workspaceId === reportAlpha.id ? "6" : "7";
  const assetPrefix = workspaceId === reportAlpha.id ? "2" : "3";
  const breached = 103, withinTarget = 5;
  return Array.from({ length: breached + withinTarget }, (_, index): SyntheticSLAFinding => {
    const severity = severities[index % severities.length];
    const targetDays = slaTarget(policy, severity);
    const status: SLAStatus = index < breached ? "breached" : "within-target";
    const offset = status === "breached" ? (index + 1) * 1_001 :
      index === breached ? 0 : -(index - breached) * 3_600;
    const dueAtMs = asOf - offset * 1_000;
    const firstObservedAtMs = dueAtMs - targetDays * 86_400_000;
    const owned = index % 3 !== 1;
    return {
      findingId: `${prefix}${(index + 1).toString(16).padStart(31, "0")}`,
      title: `Synthetic ${workspaceId === reportAlpha.id ? "Alpha" : "Beta"} SLA finding ${String(index + 1).padStart(4, "0")}`,
      assetId: `${assetPrefix}${(index + 1).toString(16).padStart(31, "0")}`,
      assetName: `Synthetic ${workspaceId === reportAlpha.id ? "Alpha" : "Beta"} SLA asset ${String(index + 1).padStart(4, "0")}`,
      severity,
      ownerId: owned ? reportUser.id : null,
      ownerName: owned ? reportUser.name : null,
      workflowState: workflows[index % workflows.length],
      disposition: dispositions[index % dispositions.length],
      sourceState: sourceStates[index % sourceStates.length],
      firstObservedAt: new Date(firstObservedAtMs).toISOString(),
      dueAt: new Date(dueAtMs).toISOString(),
      targetDays,
      status,
      overdueSeconds: status === "breached" ? Math.floor((asOf - dueAtMs) / 1_000) : 0,
    };
  });
}

export function syntheticSLASummary(workspaceId: string,
  policy = workspaceId === reportAlpha.id ? alphaSLAPolicy : betaSLAPolicy): SyntheticSLASummary {
  const findings = syntheticSLAFindingSet(workspaceId, policy);
  const bySeverity: SyntheticSLASummary["bySeverity"] = {
    critical: { tracked: 0, breached: 0 },
    high: { tracked: 0, breached: 0 },
    medium: { tracked: 0, breached: 0 },
    low: { tracked: 0, breached: 0 },
    info: { tracked: 0, breached: 0 },
  };
  for (const finding of findings) {
    bySeverity[finding.severity].tracked += 1;
    if (finding.status === "breached") bySeverity[finding.severity].breached += 1;
  }
  const breached = findings.filter((finding) => finding.status === "breached").length;
  return {
    workspaceId, asOf: "2026-10-08T17:01:51.000Z", policy: structuredClone(policy),
    totals: { tracked: findings.length, withinTarget: findings.length - breached, breached },
    bySeverity,
    verification: { state: "not-run", reason: slaVerificationReason },
  };
}

export function syntheticSLAResponse(workspaceId: string,
  policy = workspaceId === reportAlpha.id ? alphaSLAPolicy : betaSLAPolicy): SyntheticSLAResponse {
  return { apiVersion, dataOrigin: "live", sla: syntheticSLASummary(workspaceId, policy) };
}

export function syntheticSLAPolicyResponse(policy: SyntheticSLAPolicy): SyntheticSLAPolicyResponse {
  return { apiVersion, dataOrigin: "live", policy: structuredClone(policy) };
}

export function syntheticSLAFindingPage(workspaceId: string, status: SLAStatus, limit = 100, cursor = "",
  policy = workspaceId === reportAlpha.id ? alphaSLAPolicy : betaSLAPolicy): SyntheticSLAFindingPage {
  if (!["breached", "within-target"].includes(status) ||
    !Number.isInteger(limit) || limit < 1 || limit > 100 ||
    cursor !== "" && !/^[a-f0-9]{32}$/.test(cursor)) {
    throw new Error("Only bounded native SLA finding pages may be generated.");
  }
  const all = syntheticSLAFindingSet(workspaceId, policy).filter((finding) => finding.status === status);
  const remaining = all.filter((finding) => finding.findingId > cursor);
  const items = remaining.slice(0, limit);
  return {
    apiVersion, dataOrigin: "live", items: structuredClone(items), total: all.length,
    nextCursor: remaining.length > limit ? items.at(-1)!.findingId : null,
  };
}

export const slaStorageCanaries = (() => {
  const summary = syntheticSLASummary(reportAlpha.id);
  const findings = syntheticSLAFindingSet(reportAlpha.id);
  return [
    summary.asOf, summary.policy.rationale, slaVerificationReason,
    ...findings.slice(0, 4).flatMap((finding) =>
      [finding.findingId, finding.title, finding.assetId, finding.assetName,
        finding.firstObservedAt, finding.dueAt]),
  ];
})();

export function emptyReport(workspaceId = reportAlpha.id) {
  return withFreshness({
    ...initial, workspaceId,
    totals: { assets: 0, findings: 0, openFindings: 0, acceptedRisk: 0, expiredAcceptedRisk: 0,
      suppressed: 0, expiredSuppression: 0, falsePositive: 0, inferredResolved: 0, verifiedResolved: 0 },
    bySeverity: { critical: 0, high: 0, medium: 0, low: 0, info: 0 },
    coverage: { scannedAssets: 0, unscannedAssets: 0, staleAssets: 0, unknownFreshnessAssets: 0, freshnessWindowDays: 7 },
  }, 7);
}

export function savedSnapshots(workspaceId: string, count = 2): SyntheticSnapshot[] {
  if (![reportAlpha.id, reportBeta.id].includes(workspaceId) || !Number.isInteger(count) || count < 0 || count > 501) {
    throw new Error("Only the bounded synthetic Reports history may be seeded.");
  }
  const alpha = workspaceId === reportAlpha.id;
  return Array.from({ length: count }, (_, index) => {
    const report = { ...structuredClone(savedReport), workspaceId };
    return {
      id: `${alpha ? "b" : "d"}${(index + 1).toString(16).padStart(31, "0")}`, workspaceId,
      name: `Synthetic ${alpha ? "Alpha" : "Beta"} saved snapshot ${String(index + 1).padStart(4, "0")}`,
      requestedBy: reportUser.id, freshnessDays: 7, state: "succeeded",
      createdAt: "2026-08-26T05:00:00.000Z", completedAt: report.asOf, failure: null, report,
    };
  });
}

export function snapshotSummary(snapshot: SyntheticSnapshot): SyntheticSnapshotSummary {
  const { report: _report, ...summary } = snapshot;
  return structuredClone(summary);
}

export function overviewDays(url: URL): number {
  const values = url.searchParams.getAll("freshnessDays");
  if ([...url.searchParams.keys()].some((key) => key !== "freshnessDays") || values.length > 1) {
    throw new Error("Overview only accepts one freshnessDays parameter.");
  }
  if (values.length === 0) return 7;
  if (!/^\d{1,3}$/.test(values[0]) || Number(values[0]) < 1 || Number(values[0]) > 365) {
    throw new Error("Overview freshnessDays must be an integer from 1 through 365.");
  }
  return Number(values[0]);
}

export function snapshotParameters(url: URL): { limit: number; cursor: string } {
  if ([...url.searchParams.keys()].some((key) => !["limit", "cursor"].includes(key)) ||
    url.searchParams.getAll("limit").length > 1 || url.searchParams.getAll("cursor").length > 1) {
    throw new Error("Snapshot history uses only limit and cursor, not page, pageSize, filters or trends.");
  }
  const rawLimit = url.searchParams.get("limit"), cursor = url.searchParams.get("cursor") ?? "";
  const limit = rawLimit === null || rawLimit === "" ? 100 : Number(rawLimit);
  if (rawLimit !== null && rawLimit !== "" && !/^\d+$/.test(rawLimit) ||
    !Number.isInteger(limit) || limit < 1 || limit > 500 || cursor !== "" && !/^[a-f0-9]{32}$/.test(cursor)) {
    throw new Error("Snapshot history requires limit 1..500 and a lower-case 32-hex cursor.");
  }
  return { limit, cursor };
}

export function trendDays(url: URL): number {
  const values = url.searchParams.getAll("days");
  if ([...url.searchParams.keys()].some((key) => key !== "days") || values.length > 1) {
    throw new Error("Historical trends accept exactly one days parameter and no other query keys.");
  }
  if (values.length === 0) return 30;
  if (!/^\d{1,3}$/.test(values[0]) || Number(values[0]) < 1 || Number(values[0]) > 365) {
    throw new Error("Historical trend days must be one integer from 1 through 365.");
  }
  return Number(values[0]);
}

export function coverageParameters(url: URL): {
  state: CoverageState; freshnessDays: number; limit: number; cursor: string;
} {
  const allowed = ["state", "freshnessDays", "limit", "cursor"];
  const stateValues = url.searchParams.getAll("state");
  const freshnessValues = url.searchParams.getAll("freshnessDays");
  const limitValues = url.searchParams.getAll("limit");
  const cursorValues = url.searchParams.getAll("cursor");
  if ([...url.searchParams.keys()].some((key) => !allowed.includes(key)) ||
    stateValues.length !== 1 || freshnessValues.length !== 1 ||
    limitValues.length > 1 || cursorValues.length > 1) {
    throw new Error("Coverage drill-down requires state and freshnessDays and accepts only one native limit and cursor.");
  }
  const state = stateValues[0] as CoverageState;
  if (!["scanned", "unscanned", "stale", "unknown-freshness"].includes(state)) {
    throw new Error("Coverage drill-down state must be one exact Live overview coverage metric.");
  }
  if (!/^\d{1,3}$/.test(freshnessValues[0]) ||
    Number(freshnessValues[0]) < 1 || Number(freshnessValues[0]) > 365) {
    throw new Error("Coverage drill-down freshnessDays must be one integer from 1 through 365.");
  }
  const rawLimit = limitValues[0], limit = rawLimit === undefined ? 100 : Number(rawLimit);
  if (rawLimit !== undefined && !/^\d{1,3}$/.test(rawLimit) ||
    !Number.isInteger(limit) || limit < 1 || limit > 100) {
    throw new Error("Coverage drill-down limit must be one integer from 1 through 100.");
  }
  const cursor = cursorValues[0] ?? "";
  if (cursorValues.length === 1 && !/^[a-f0-9]{32}$/.test(cursor)) {
    throw new Error("Coverage drill-down cursor must be one lower-case 32-hex asset ID.");
  }
  return { state, freshnessDays: Number(freshnessValues[0]), limit, cursor };
}

export function noSLAQuery(url: URL, label: string) {
  if ([...url.searchParams.keys()].length !== 0) {
    throw new Error(`${label} accepts no query parameters.`);
  }
}

export function slaFindingParameters(url: URL): { status: SLAStatus; limit: number; cursor: string } {
  const allowed = ["status", "limit", "cursor"];
  const statusValues = url.searchParams.getAll("status");
  const limitValues = url.searchParams.getAll("limit");
  const cursorValues = url.searchParams.getAll("cursor");
  if ([...url.searchParams.keys()].some((key) => !allowed.includes(key)) ||
    statusValues.length !== 1 || limitValues.length > 1 || cursorValues.length > 1) {
    throw new Error("SLA finding pages require one status and at most one native limit and cursor.");
  }
  const status = statusValues[0] as SLAStatus;
  if (!["breached", "within-target"].includes(status)) {
    throw new Error("SLA finding status must be breached or within-target.");
  }
  const rawLimit = limitValues[0], limit = rawLimit === undefined ? 100 : Number(rawLimit);
  if (rawLimit !== undefined && !/^\d{1,3}$/.test(rawLimit) ||
    !Number.isInteger(limit) || limit < 1 || limit > 100) {
    throw new Error("SLA finding limit must be one integer from 1 through 100.");
  }
  const cursor = cursorValues[0] ?? "";
  if (cursorValues.length === 1 && !/^[a-f0-9]{32}$/.test(cursor)) {
    throw new Error("SLA finding cursor must be one lower-case 32-hex finding ID.");
  }
  return { status, limit, cursor };
}
