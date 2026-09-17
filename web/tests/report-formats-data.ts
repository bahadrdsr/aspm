import { createHash } from "node:crypto";
import { apiVersion } from "./api-contract";
import { backendID, validExpiry } from "./finding-actions-data";

export { apiVersion, backendID, validExpiry };
export const uploadLimit = 8 << 20;
export const profiles = ["sarif", "trivy", "zap", "gitleaks", "generic-json", "generic-csv", "manual"] as const;
export type ReportProfile = typeof profiles[number];
export type IntakeRole = "admin" | "analyst" | "viewer";
export type IntakeState = "queued" | "processing" | "succeeded" | "failed";
export const intakeAlpha = { id: "13000000000000000000000000000001", name: "Synthetic report Alpha" };
export const intakeBeta = { id: "13000000000000000000000000000002", name: "Synthetic report Beta" };
export const intakeUser = { id: "23000000000000000000000000000001", name: "Synthetic intake analyst", email: "report-formats@synthetic.invalid" };
export const intakeCookie = "f".repeat(43);
export const firstAsset = {
  id: "33000000000000000000000000000001", workspaceId: intakeAlpha.id, name: "Synthetic first inventory asset",
  kind: "repository", environment: "test", criticality: "medium", tags: ["synthetic"], ownerId: null,
};
export const selectedAsset = { ...firstAsset, id: "33000000000000000000000000000002", name: "Synthetic selected café repository" };
export const betaAsset = {
  ...firstAsset, id: "33000000000000000000000000000003", workspaceId: intakeBeta.id, name: "Synthetic Beta inventory only",
};
export const literalMapping = {
  sourceFindingId: "id", title: "title", sourceSeverity: "severity", sourceLocation: "path",
  sourceLine: "line", impact: "impact", remediation: "remediation", description: "description",
};
export const importsPath = "/api/v1/imports";
export const assetsPath = "/api/v1/assets";
export const sourceScanTime = "2026-09-02T03:04:05+02:00";
export const acceptedAt = "2026-09-17T01:02:03.456789Z";
export interface IntakeMetadata {
  assetId: string; sourceId: string; scanId: string; scope: { id: string; revision: string; branch: string };
  sourceScanAt: string | null; sourceStatus: "succeeded" | "failed"; scanKind: "full" | "delta";
  completeness: "complete" | "partial" | "unknown";
}
export interface IntakeBody extends IntakeMetadata {
  apiVersion: typeof apiVersion; format: ReportProfile; report: string; collectedAt: string;
  mapping?: typeof literalMapping;
}
export interface IntakeReceipt extends IntakeMetadata {
  id: string; runId: string; state: IntakeState; format: ReportProfile; collectedAt: string; importedAt: string;
  reportDigest: string; observationCount: number; failure: { code: string; message: string; retryable: false } | null;
}
export interface ReportFile { format: ReportProfile; name: string; mimeType: string; buffer: Buffer }
export function metadata(scan: string, changes: Partial<IntakeMetadata> = {}): IntakeMetadata {
  return {
    assetId: selectedAsset.id, sourceId: "synthetic-format-source", scanId: `synthetic-format-${scan}`,
    scope: { id: "synthetic-report-scope", revision: "fixture-revision-2", branch: "refs/heads/synthetic-main" },
    sourceScanAt: sourceScanTime, sourceStatus: "succeeded", scanKind: "full", completeness: "complete", ...changes,
  };
}
export function digest(bytes: Buffer | string) { return `sha256:${createHash("sha256").update(bytes).digest("hex")}`; }
export function exactKeys(value: object, names: string[]) { return Object.keys(value).sort().join(",") === [...names].sort().join(","); }
export function validText(value: unknown, limit: number): value is string {
  const blank = /^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]*$/u;
  return typeof value === "string" && !blank.test(value) && !value.includes("\0") && Buffer.byteLength(value) <= limit;
}
export function utc(value: string) {
  return new Date(value).toISOString().replace(/\.000Z$/, "Z").replace(/(\.\d*?[1-9])0+Z$/, "$1Z");
}
function jsonFile(format: ReportProfile, value: unknown, name = `synthetic-${format}.json`): ReportFile {
  return { format, name, mimeType: "application/json", buffer: Buffer.from(JSON.stringify(value, null, 2).replaceAll("\n", "\r\n") + "\r\n\r\n") };
}
export const trivyFile = jsonFile("trivy", {
  SchemaVersion: 2,
  Results: [{ Target: "synthetic-image (fixture OS)", Vulnerabilities: [{
    VulnerabilityID: "SYN-REPORT-TRIVY-1", PkgName: "synthetic-lib", InstalledVersion: "1.0.0", FixedVersion: "1.0.1",
    Severity: "HIGH", Title: "Synthetic café package observation", Description: "Synthetic impact only - 安全.",
    VendorNote: "Literal synthetic native report context.",
  }] }],
}, "synthetic-trivy-misleading-name.sarif");
export const zapFile = jsonFile("zap", {
  "@version": "2.16.1", "@generated": "Synthetic generation label, not source scan time",
  site: [{ alerts: [{
    alertRef: "SYN-REPORT-ZAP-1", alert: "Synthetic café header policy", riskcode: "2",
    desc: "Synthetic response context - 安全.", solution: "Review the synthetic fixture.",
    instances: [{ uri: "https://report-formats.synthetic.invalid/health", evidence: "Synthetic header evidence, not a test request." }],
  }] }],
});
export const gitleaksFile = jsonFile("gitleaks", [{
  Fingerprint: "synthetic-commit:config/synthetic.txt:synthetic-rule:4", Description: "Synthetic redacted café report - 安全.",
  File: "config/synthetic.txt", StartLine: 4, RuleID: "synthetic-rule", Commit: "0".repeat(40),
  Date: "2026-08-01T01:02:03Z", Secret: "[REDACTED SYNTHETIC]", Match: "[REDACTED SYNTHETIC]",
}]);
export const manualFile = jsonFile("manual", {
  sourceFindingId: "synthetic-manual-1", title: "Synthetic human-authored café observation", severity: "high",
  sourceLocation: { uri: "src/synthetic.ts", line: 7 }, description: "Structured human input - 安全.\nSecond literal line.",
  impact: "Synthetic context only.", remediation: "Review this synthetic record.", extra: "Not AI-extracted prose.",
});
const csvHeader = "id,title,severity,path,line,impact,remediation,description,extra\r\n";
const csvPrefix = 'synthetic-csv-1,"Synthetic café, ""quoted"" observation",HIGH,src/synthetic.ts,7,Synthetic context.,Review fixture.,"First line\r\nSecond line - 安全.",';
export const csvFile: ReportFile = {
  format: "generic-csv", name: "synthetic-quoted-report.csv", mimeType: "text/csv",
  buffer: Buffer.from(csvHeader + csvPrefix + '"Literal extra, ""retained"" text"\r\n\r\n'),
};
export const manualProseFile: ReportFile = {
  format: "manual", name: "synthetic-not-structured-manual.txt", mimeType: "text/plain",
  buffer: Buffer.from("Synthetic café human prose is not a structured manual JSON report.\r\nDo not convert this into a finding - 安全.\r\n"),
};
export function csvWithExtra(extra: string, name: string): ReportFile {
  return { format: "generic-csv", name, mimeType: "text/csv", buffer: Buffer.from(csvHeader + csvPrefix + `"${extra}"\r\n`) };
}
export function nearLimitCSV(): ReportFile {
  const empty = csvWithExtra("", "synthetic-large-valid.csv");
  return csvWithExtra("x".repeat(uploadLimit - 4096 - empty.buffer.length), empty.name);
}
export function encodedOverflowCSV(): ReportFile {
  return csvWithExtra('""'.repeat((uploadLimit >>> 2) + 4096), "synthetic-escaped-wrapper-overflow.csv");
}
export const invalidUTF8File: ReportFile = {
  format: "generic-csv", name: "synthetic-invalid-utf8.csv", mimeType: "text/csv",
  buffer: Buffer.concat([Buffer.from(csvHeader + csvPrefix), Buffer.from([0xc3, 0x28]), Buffer.from("\r\n")]),
};
