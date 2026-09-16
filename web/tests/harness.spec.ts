import { expect, test } from "@playwright/test";
import { build } from "vite";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { gitBlobID, requireApprovedProvider, requireSourceApproval, verifyApprovedLicense } from "./source-approval";
import type { ApprovedSource, SourceApprovalRegistry } from "./source-approval";

test("harness compiles React and exercises real browser keyboard events", async ({ page }) => {
  await page.goto("/tests/harness/index.html");
  await expect(page.getByRole("heading", { name: "M01 test harness only" })).toBeVisible();
  const button = page.getByRole("button", { name: "Probe React events" });
  await page.keyboard.press("Tab");
  await expect(button).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByLabel("Harness activations")).toHaveText("1");
  await expect(page.getByText("This synthetic probe is not the product UI or a fallback for it.")).toBeVisible();
});

test("harness produces static assets without needing the production UI", async () => {
  const root = fileURLToPath(new URL("./harness", import.meta.url));
  const configFile = fileURLToPath(new URL("../vite.config.ts", import.meta.url));
  const result = await build({
    root,
    configFile,
    logLevel: "silent",
    build: { write: false, emptyOutDir: false },
  });
  const outputs = Array.isArray(result) ? result : [result];
  expect(outputs.some((output) => "output" in output && output.output.some((file) => file.type === "chunk"))).toBe(true);
});

test("approval guard distinguishes reviewed license candidates from approved components", () => {
  const license = "SYNTHETIC HARNESS LICENSE TEXT - NOT A REAL SOURCE APPROVAL\n";
  const licenseBytes = Buffer.from(license);
  const hash = (text: string) => createHash("sha256").update(text).digest("hex");
  expect(gitBlobID(Buffer.from("test content\n"))).toBe("d670460b4b4aece5915caf5c68d12f560a9fe3e4");
  // This in-memory fixture is not added to the coordinator-owned approval registry.
  const source: ApprovedSource = {
    provider: "shadcn",
    repository: "shadcn-ui/ui",
    commit: createHash("sha1").update("synthetic non-repository fixture").digest("hex"),
    path: "__synthetic_harness__/not-a-component.tsx",
    sourceSHA256: hash("synthetic source, never fetched or copied"),
    license: {
      expression: "MIT",
      path: "__synthetic_harness__/LICENSE",
      gitBlob: gitBlobID(licenseBytes),
      snapshotPath: "__synthetic_harness__/not-a-license.txt",
      sha256: hash(license),
      additionalTerms: [],
    },
    approval: {
      status: "approved",
      authority: "coordinator",
      foss: true,
      perFileLicenseReviewed: true,
      dependencyClosureReviewed: true,
      reviewRef: "__synthetic_harness__/not-a-review.txt",
    },
  };
  expect(() => requireSourceApproval(source, [])).toThrow(/approved-source gate blocked/);
  expect(requireSourceApproval(source, [source])).toBe(source);
  expect(() => verifyApprovedLicense(licenseBytes, source)).not.toThrow();
  expect(() => verifyApprovedLicense(Buffer.from("different synthetic license"), source)).toThrow();
  expect(() => verifyApprovedLicense(Buffer.from(license.replaceAll("\n", "\r\n")), source)).toThrow(/blob/);
  expect(() => requireSourceApproval(source, [source, source])).toThrow();
  expect(() => requireSourceApproval({ ...source, commit: "main" }, [source])).toThrow();
  const floatingRevision = { ...source, commit: "main" };
  expect(() => requireSourceApproval(floatingRevision, [floatingRevision])).toThrow();
  expect(() => requireSourceApproval({ ...source, sourceSHA256: hash("different source") }, [source])).toThrow();
  const restricted = structuredClone(source);
  restricted.license.additionalTerms = ["Commons Clause"];
  expect(() => requireSourceApproval(restricted, [restricted])).toThrow();
  restricted.license.additionalTerms = [];
  restricted.license.expression = "MIT + Commons Clause";
  expect(() => requireSourceApproval(restricted, [restricted])).toThrow();
  const restrictedText = `${license}\nCommons Clause`;
  const mislabeled = structuredClone(source);
  mislabeled.license.sha256 = hash(restrictedText);
  mislabeled.license.gitBlob = gitBlobID(Buffer.from(restrictedText));
  expect(() => verifyApprovedLicense(Buffer.from(restrictedText), mislabeled)).toThrow(/Commons Clause/);
  const unreviewed = structuredClone(source);
  unreviewed.approval.foss = false;
  expect(() => requireSourceApproval(unreviewed, [unreviewed])).toThrow();
  unreviewed.approval.foss = true;
  unreviewed.approval.perFileLicenseReviewed = false;
  expect(() => requireSourceApproval(unreviewed, [unreviewed])).toThrow();
  unreviewed.approval.perFileLicenseReviewed = true;
  unreviewed.approval.dependencyClosureReviewed = false;
  expect(() => requireSourceApproval(unreviewed, [unreviewed])).toThrow();
  const registry = JSON.parse(readFileSync(new URL("./approved-component-sources.json", import.meta.url), "utf8")) as SourceApprovalRegistry;
  const candidate = registry.licenseReviewedCandidates.find((item) => item.provider === "animate-ui");
  if (!candidate) throw new Error("The coordinator's historical license candidate must be recorded separately from component approvals.");
  expect(candidate.commit).toBe("38b917762e3b6059c06a7af703071ba11a89091e");
  expect(candidate.license.gitBlob).toBe("0ca4a48bd712fd8db31fff20c8b241c5cf41e4db");
  expect(candidate.license.expression).toBe("MIT");
  expect(candidate.review.fullTextReviewed).toBe(true);
  expect(candidate.review.unrestrictedPermissionAndSellConfirmed).toBe(true);
  const licenseOnly: ApprovedSource = {
    ...structuredClone(source),
    provider: "animate-ui",
    repository: candidate.repository,
    commit: candidate.commit,
    license: { ...source.license, path: candidate.license.path, gitBlob: candidate.license.gitBlob },
    approval: { ...source.approval, perFileLicenseReviewed: false, dependencyClosureReviewed: false },
  };
  expect(() => requireSourceApproval(licenseOnly, registry.approvedSources, registry.licenseReviewedCandidates)).toThrow();
  expect(() => requireSourceApproval(licenseOnly, [licenseOnly], registry.licenseReviewedCandidates)).toThrow();
  const reviewedMetadataOnly = { ...licenseOnly, approval: { ...source.approval } };
  expect(requireSourceApproval(reviewedMetadataOnly, [reviewedMetadataOnly], registry.licenseReviewedCandidates)).toBe(reviewedMetadataOnly);
  expect(() => verifyApprovedLicense(licenseBytes, reviewedMetadataOnly)).toThrow(/blob/);
  const otherRevision = {
    ...licenseOnly,
    commit: hash("synthetic different revision").slice(0, 40),
    approval: { ...source.approval },
  };
  expect(() => requireSourceApproval(otherRevision, [otherRevision], registry.licenseReviewedCandidates)).toThrow(/immutable candidate/);
  const wrongBlob = {
    ...licenseOnly,
    license: { ...licenseOnly.license, gitBlob: gitBlobID(licenseBytes) },
    approval: { ...source.approval },
  };
  expect(() => requireSourceApproval(wrongBlob, [wrongBlob], registry.licenseReviewedCandidates)).toThrow(/immutable candidate/);
  for (const approved of registry.approvedSources) {
    expect(approved.path).not.toContain("__synthetic_harness__");
    expect(approved.commit).not.toBe(source.commit);
  }
  expect(() => requireApprovedProvider("animate-ui", { ...registry, approvedSources: [] })).toThrow(/approved-source gate blocked/);
});
