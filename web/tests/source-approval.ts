import { createHash } from "node:crypto";

export type SourceProvider = "shadcn" | "animate-ui";

export interface SourceClaim {
  provider: SourceProvider;
  repository: string;
  commit: string;
  path: string;
  sourceSHA256: string;
}

export interface ApprovedSource extends SourceClaim {
  license: {
    expression: string;
    path: string;
    gitBlob: string;
    snapshotPath: string;
    sha256: string;
    additionalTerms: string[];
  };
  approval: {
    status: "approved";
    authority: "coordinator" | "independent-reviewer";
    foss: boolean;
    perFileLicenseReviewed: boolean;
    dependencyClosureReviewed: boolean;
    reviewRef: string;
  };
}

export interface LicenseReviewedCandidate {
  provider: SourceProvider;
  repository: string;
  commit: string;
  license: {
    expression: string;
    path: string;
    gitBlob: string;
    additionalTerms: string[];
  };
  review: {
    status: "license-reviewed";
    authority: "coordinator" | "independent-reviewer";
    fullTextReviewed: boolean;
    unrestrictedPermissionAndSellConfirmed: boolean;
  };
}

export interface SourceApprovalRegistry {
  schemaVersion: number;
  policy: string;
  licenseReviewedCandidates: LicenseReviewedCandidate[];
  approvedSources: ApprovedSource[];
}

const restrictedTerms = /commons[\s-]*clause/i;

export function requireSourceApproval(
  claim: SourceClaim,
  sources: ApprovedSource[],
  licenseCandidates: LicenseReviewedCandidate[] = [],
): ApprovedSource {
  const matches = sources.filter((source) =>
    source.provider === claim.provider &&
    source.repository === claim.repository &&
    source.commit === claim.commit &&
    source.path === claim.path &&
    source.sourceSHA256 === claim.sourceSHA256,
  );
  if (matches.length !== 1) {
    throw new Error(`M01 approved-source gate blocked: ${claim.provider} has no unique independent approval for this exact source.`);
  }
  const source = matches[0];
  const license = source.license;
  const review = source.approval;
  const expectedRepository = claim.provider === "shadcn" ? "shadcn-ui/ui" : "imskyleen/animate-ui";
  if (
    claim.repository !== expectedRepository ||
    !/^[a-f0-9]{40}$/.test(claim.commit) ||
    !/^[a-f0-9]{64}$/.test(claim.sourceSHA256) ||
    !claim.path.trim() ||
    review.status !== "approved" ||
    !["coordinator", "independent-reviewer"].includes(review.authority) ||
    review.foss !== true ||
    review.perFileLicenseReviewed !== true ||
    review.dependencyClosureReviewed !== true ||
    !review.reviewRef.trim() ||
    !license.expression.trim() ||
    !license.path.trim() ||
    !/^[a-f0-9]{40}$/.test(license.gitBlob) ||
    !license.snapshotPath.trim() ||
    !/^[a-f0-9]{64}$/.test(license.sha256) ||
    !Array.isArray(license.additionalTerms) ||
    license.additionalTerms.length !== 0 ||
    restrictedTerms.test(license.expression) ||
    (claim.provider === "animate-ui" && license.expression !== "MIT")
  ) {
    throw new Error("M01 approved-source gate blocked: immutable source, full license and independent FOSS/dependency review are required.");
  }
  if (claim.provider === "animate-ui") {
    const reviewed = licenseCandidates.filter((candidate) =>
      candidate.provider === claim.provider &&
      candidate.repository === claim.repository &&
      candidate.commit === claim.commit &&
      candidate.license.path === license.path &&
      candidate.license.gitBlob === license.gitBlob &&
      candidate.license.expression === "MIT" &&
      candidate.license.additionalTerms.length === 0 &&
      candidate.review.status === "license-reviewed" &&
      ["coordinator", "independent-reviewer"].includes(candidate.review.authority) &&
      candidate.review.fullTextReviewed === true &&
      candidate.review.unrestrictedPermissionAndSellConfirmed === true,
    );
    if (reviewed.length !== 1) {
      throw new Error("M01 approved-source gate blocked: Animate UI commit/license blob is not the independently reviewed immutable candidate.");
    }
  }
  return source;
}

export function requireApprovedProvider(provider: SourceProvider, registry: SourceApprovalRegistry): void {
  if (registry.schemaVersion !== 1 || registry.policy !== "independent-immutable-foss-approval") {
    throw new Error("M01 approved-source gate blocked: invalid approval registry.");
  }
  const candidates = registry.approvedSources.filter((source) => source.provider === provider);
  if (candidates.length === 0) {
    throw new Error(
      `M01 approved-source gate blocked: ${provider} still needs per-file and dependency-closure approval. ` +
      "A reviewed historical root license is not component adoption approval; current Animate UI main/registry dependencies remain unapproved.",
    );
  }
  for (const source of candidates) requireSourceApproval(source, registry.approvedSources, registry.licenseReviewedCandidates);
}

export function gitBlobID(bytes: Uint8Array): string {
  return createHash("sha1").update(`blob ${bytes.byteLength}\0`).update(bytes).digest("hex");
}

export function verifyApprovedLicense(bytes: Uint8Array, source: ApprovedSource): void {
  const normalized = Buffer.from(bytes).toString("utf8").replaceAll("\r\n", "\n");
  const actual = createHash("sha256").update(normalized).digest("hex");
  if (restrictedTerms.test(normalized) || actual !== source.license.sha256 || gitBlobID(bytes) !== source.license.gitBlob) {
    throw new Error("M01 approved-source gate blocked: license text/blob differs from approval or includes Commons Clause.");
  }
}
