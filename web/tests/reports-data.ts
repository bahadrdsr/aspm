import { apiVersion } from "./api-contract";

export type ReportRole = "admin" | "analyst" | "viewer";
export type SnapshotState = "queued" | "processing" | "succeeded" | "failed";
export interface SyntheticPostureReport {
  workspaceId: string;
  asOf: string;
  totals: {
    assets: number; findings: number; openFindings: number; acceptedRisk: number;
    expiredAcceptedRisk: number; inferredResolved: number; verifiedResolved: number;
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

export const reportAlpha = { id: "1".repeat(32), name: "Synthetic Reports Alpha workspace", role: "admin" as ReportRole };
export const reportBeta = { id: "3".repeat(32), name: "Synthetic Reports Beta workspace", role: "analyst" as ReportRole };
export const reportUser = { id: "a".repeat(32), name: "Synthetic reporting analyst", email: "reports@synthetic.invalid" };
export const verificationReason = "Independent verified-resolution results are not integrated with canonical findings.";
export const overviewPath = "/api/v1/reports/overview";
export const snapshotsPath = "/api/v1/reports/snapshots";

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
  totals: { assets: 127, findings: 263, openFindings: 181, acceptedRisk: 37, expiredAcceptedRisk: 11, inferredResolved: 29, verifiedResolved: 0 },
  bySeverity: { critical: 17, high: 43, medium: 89, low: 101, info: 13 },
  coverage: { scannedAssets: 91, unscannedAssets: 36, staleAssets: 23, unknownFreshnessAssets: 7, freshnessWindowDays: 7 },
  freshnessWindow: { from: "", to: "", days: 7 },
  verification: { state: "not-run", reason: verificationReason },
};

export const alphaOverview = withFreshness(initial, 7);
export const betaOverview = withFreshness({
  ...initial, workspaceId: reportBeta.id, asOf: "2026-09-13T04:05:06.000Z",
  totals: { assets: 908, findings: 1493, openFindings: 1021, acceptedRisk: 109, expiredAcceptedRisk: 19, inferredResolved: 137, verifiedResolved: 0 },
  bySeverity: { critical: 83, high: 149, medium: 227, low: 401, info: 633 },
  coverage: { scannedAssets: 821, unscannedAssets: 87, staleAssets: 217, unknownFreshnessAssets: 53, freshnessWindowDays: 7 },
}, 7);
export const refreshedOverview = withFreshness({
  ...initial, asOf: "2026-09-13T13:35:57.000Z",
  totals: { assets: 211, findings: 419, openFindings: 277, acceptedRisk: 61, expiredAcceptedRisk: 19, inferredResolved: 47, verifiedResolved: 0 },
  bySeverity: { critical: 31, high: 67, medium: 97, low: 113, info: 111 },
  coverage: { scannedAssets: 173, unscannedAssets: 38, staleAssets: 41, unknownFreshnessAssets: 13, freshnessWindowDays: 7 },
}, 7);
export const savedReport = withFreshness({
  ...initial, asOf: "2026-08-26T05:06:07.000Z",
  totals: { assets: 71, findings: 149, openFindings: 103, acceptedRisk: 23, expiredAcceptedRisk: 5, inferredResolved: 17, verifiedResolved: 0 },
  bySeverity: { critical: 11, high: 19, medium: 31, low: 41, info: 47 },
  coverage: { scannedAssets: 61, unscannedAssets: 10, staleAssets: 13, unknownFreshnessAssets: 3, freshnessWindowDays: 7 },
}, 7);

export function emptyReport(workspaceId = reportAlpha.id) {
  return withFreshness({
    ...initial, workspaceId,
    totals: { assets: 0, findings: 0, openFindings: 0, acceptedRisk: 0, expiredAcceptedRisk: 0, inferredResolved: 0, verifiedResolved: 0 },
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
