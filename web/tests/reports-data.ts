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

export const reportAlpha = { id: "1".repeat(32), name: "Synthetic Reports Alpha workspace", role: "admin" as ReportRole };
export const reportBeta = { id: "3".repeat(32), name: "Synthetic Reports Beta workspace", role: "analyst" as ReportRole };
export const reportUser = { id: "a".repeat(32), name: "Synthetic reporting analyst", email: "reports@synthetic.invalid" };
export const verificationReason = "Independent verified-resolution results are not integrated with canonical findings.";
export const trendVerificationReason = "Historical snapshot trends are not an SLA, forecast, or independent verification.";
export const overviewPath = "/api/v1/reports/overview";
export const snapshotsPath = "/api/v1/reports/snapshots";
export const trendsPath = "/api/v1/reports/trends";

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
