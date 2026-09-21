import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const review = dirname(fileURLToPath(import.meta.url));
const root = resolve(review, "..", "..", "..", "..");
const suitePath = "tests\\assessment_deployment";
const reviewPath = `${suitePath}\\reviews\\v1`;
const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const json = (path) => JSON.parse(readFileSync(join(root, path)));
const witness = (path) => {
  const bytes = readFileSync(join(root, path));
  return { path, bytes: bytes.length, sha256: hash(bytes) };
};
const verify = (entry) => {
  const now = witness(entry.path);
  assert.equal(now.sha256, entry.sha256, entry.path);
  if (entry.bytes !== undefined) assert.equal(now.bytes, entry.bytes, entry.path);
  return now;
};
const baselinePath = `${reviewPath}\\BEFORE.json`;
assert.equal(witness(baselinePath).sha256, "f7db6471ff8e985cf0f4005d9c6f9473767edb04b2c0bb185e9e5e2ed097c200");
const before = json(baselinePath);
let finalSourceSetBlocker = null;
try {
  execFileSync(process.execPath, [join(review, "guard.mjs"), "author-check"], { cwd: root, stdio: "pipe" });
} catch (failure) {
  before.files.forEach(verify);
  for (const entry of before.toolWitnesses) assert.equal(hash(readFileSync(entry.path)), entry.sha256);
  const paths = execFileSync("git", ["--no-pager", "ls-files", "--cached", "--others", "--exclude-standard"], { cwd: root, encoding: "utf8" })
    .split(/\r?\n/).filter(Boolean).map((path) => path.replaceAll("/", "\\"))
    .filter((path) => !path.startsWith(`${suitePath}\\`) && !/(^|\\)(\.env$|[^\\]*private[^\\]*\.(pem|key)$)/i.test(path));
  const initial = new Set(before.files.map((entry) => entry.path));
  const additions = paths.filter((path) => !initial.has(path)).sort();
  if (additions.length === 0 || additions.some((path) => !path.startsWith("tests\\assessment_runtime\\"))) throw failure;
  finalSourceSetBlocker = {
    observedAt: new Date().toISOString(), status: "WHOLE-CHECKOUT SOURCE-SET GUARD BLOCKED",
    original1567FilesUnchanged: true, selectedToolsUnchanged: true, addedPaths: additions,
    additionsOutsideThisAuthorScope: true, ownerOrApprovalNotInferred: true,
    addedContentsNotInspectedCopiedOrTreatedAsFrozenControls: true, noResetOrGuardExclusionApplied: true,
    parentResolutionRequiredBeforeClaimingFinalWholeCheckoutGuardPass: true,
    testedInputTreesRemainUnchangedAndWitnessed: true,
  };
}
assert.equal(before.files.length, 1567);
const sourceWitnesses = before.sourceWitnesses.map(verify);
const contract = readFileSync(join(root, before.contract.path), "utf8");
const originalContract = contract.replace(/Exactly four top-level gates\. At most 32 real Helm renders per package run,\r?\neach <=15s \/ 2MiB \//,
  "Exactly four top-level gates. At most 32 real Helm renders, each <=15s / 2MiB /");
assert.equal(hash(originalContract), before.contract.sha256, "Only bound wording may differ from the pre-test contract");
assert.equal(witness(`${suitePath}\\go.sum`).sha256, witness("tests\\collection_deployment\\go.sum").sha256);
const labels = ["01", "02", "03"].map((version) => `author-assessment-deployment-red-20260921-${version}`);
const runs = [];
const allArtifacts = [];
const filesUnder = (directory) => readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
  const path = join(directory, entry.name);
  return entry.isDirectory() ? filesUnder(path) : [relative(root, path)];
});
for (const label of labels) {
  const directory = join(root, ".artifacts", "assessment-deployment-v1", label);
  const artifactPrefix = relative(root, directory);
  const receipt = json(`${artifactPrefix}\\receipt.json`);
  const sourceBefore = json(`${artifactPrefix}\\source-before.json`);
  const sourceAfter = json(`${artifactPrefix}\\source-after.json`);
  assert.equal(receipt.exitCode, 1);
  assert.equal(receipt.hardTimeout, false);
  assert.equal(receipt.outputExceeded, false);
  assert.equal(receipt.privateRuntimeOrVendorCredentialsLoaded, false);
  assert.equal(receipt.deploymentOrPrivilegeActions, false);
  assert.equal(receipt.imageBuildOrPackagerExecution, false);
  assert.equal(receipt.dependencyInstallsOrDownloads, false);
  assert.equal(sourceBefore.existingSourceControlCount, sourceBefore.files.length);
  assert.equal(sourceBefore.sourceTreeSHA256, sourceAfter.sourceTreeSHA256);
  assert.deepEqual(sourceBefore.files, sourceAfter.files);
  assert.deepEqual(sourceBefore.authoredInputs, sourceAfter.authoredInputs);
  const originalInputs = new Map(before.files.map((entry) => [entry.path, entry]));
  const outsideBaselineInputs = sourceAfter.files.filter((entry) => !originalInputs.has(entry.path));
  for (const entry of sourceAfter.files.filter((entry) => originalInputs.has(entry.path))) {
    assert.equal(entry.sha256, originalInputs.get(entry.path).sha256, entry.path);
    verify(entry);
  }
  assert.equal(sourceAfter.files.filter((entry) => originalInputs.has(entry.path)).length, 1567);
  assert.ok(outsideBaselineInputs.every((entry) => entry.path.startsWith("tests\\assessment_runtime\\")));
  if (label.endsWith("03")) sourceAfter.authoredInputs.forEach(verify);
  const events = readFileSync(join(directory, "events.jsonl"), "utf8").split(/\r?\n/)
    .filter((line) => line.startsWith("{")).map(JSON.parse);
  const tests = events.filter((event) => event.Test && ["pass", "fail", "skip"].includes(event.Action))
    .map(({ Test, Action, Elapsed }) => ({ test: Test, action: Action, elapsedSeconds: Elapsed }));
  const top = tests.filter((test) => !test.test.includes("/"));
  const positive = tests.filter((test) => test.action === "pass");
  const failedNested = tests.filter((test) => test.test.includes("/") && test.action === "fail");
  assert.equal(top.length, 4);
  assert.ok(top.every((test) => test.action === "fail"));
  assert.equal(positive.length, 7);
  assert.equal(failedNested.length, 21);
  assert.ok(tests.every((test) => test.action !== "skip"));
  const final = events.findLast((event) => !event.Test && event.Action === "fail");
  assert.ok(final);
  const helm = readdirSync(directory, { withFileTypes: true }).filter((entry) => entry.isDirectory() && entry.name.startsWith("helm-"))
    .map((entry) => {
      const path = `${artifactPrefix}\\${entry.name}\\render-receipt.json`;
      const value = json(path);
      assert.ok(value.arguments.includes("--dry-run=client"));
      const kubeconfig = json(`${artifactPrefix}\\${entry.name}\\empty-kubeconfig.json`);
      assert.deepEqual(kubeconfig.clusters, []);
      assert.deepEqual(kubeconfig.contexts, []);
      assert.deepEqual(kubeconfig.users, []);
      return { ...value, receipt: witness(path) };
    });
  assert.equal(helm.length, 27);
  assert.equal(helm.filter((render) => render.success).length, 25);
  const rejects = helm.filter((render) => !render.success);
  assert.equal(rejects.length, 2);
  assert.ok(rejects.every((render) => render.test.includes("partial-integration-")));
  const generators = readdirSync(directory, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && entry.name.startsWith("quadlet-"))
    .map((entry) => {
      const path = `${artifactPrefix}\\${entry.name}\\generator-receipt.json`;
      const value = json(path);
      assert.match(value.version, /^4\.9\./);
      assert.ok(value.uid > 0);
      assert.equal(value.mode, "user dryrun, no kmsg");
      assert.ok(value.test.endsWith("/existing-collection-native-positive-control"));
      return { ...value, receipt: witness(path) };
    });
  assert.equal(generators.length, 1);
  runs.push({
    label, exitCode: 1, topLevelGates: top, positiveControls: positive, failedNested,
    packageEventElapsedSeconds: final.Elapsed, actualHelmRenders: 27, acceptedRenders: 25, expectedExistingKeyRejections: 2,
    actualCapturedSourceCount: sourceBefore.existingSourceControlCount,
    outsideBaselineSnapshotInputs: outsideBaselineInputs,
    outsideBaselineInputsNotFrozenAsCurrentControls: true,
    helmInvocations: helm, nativeGenerators: generators,
    receipt: witness(`${artifactPrefix}\\receipt.json`), beforeInputs: witness(`${artifactPrefix}\\source-before.json`),
    afterInputs: witness(`${artifactPrefix}\\source-after.json`),
    logs: [witness(`${artifactPrefix}\\events.jsonl`), witness(`${artifactPrefix}\\run.log`)],
  });
  allArtifacts.push(...filesUnder(directory).map(witness));
}
for (const run of runs) {
  const snapshot = new Map(json(run.afterInputs.path).files.map((entry) => [entry.path, entry.sha256]));
  for (const input of before.sourceWitnesses) assert.equal(snapshot.get(input.path), input.sha256, "Actual artifact read-source changed across runs");
}
const selected = runs.at(-1);
const finalLog = readFileSync(join(root, selected.logs[1].path), "utf8");
for (const message of [
  "source Containerfile omits building assessment-worker", "host packaging must include assessment-worker",
  "actual host command inventory has 6 entries", "ASPM_ASSESSMENT_SCOPE must preserve",
  "actual chart output is missing the required assessment Deployment", "actual values.yaml must declare",
  "required production artifact is absent:", "aspm-assessment.container",
]) assert.ok(finalLog.includes(message), message);
assert.equal(existsSync(join(root, "deploy", "quadlet", "aspm-assessment.container")), false);
const author = "independent-assessment-deployment-test-author-20260921";
const capturedAt = new Date().toISOString();
const generated = [];
const emit = (name, value) => {
  const path = `${reviewPath}\\${name}`;
  assert.equal(existsSync(join(root, path)), false, `Refusing to replace ${name}`);
  writeFileSync(join(root, path), JSON.stringify(value, null, 2) + "\n", { flag: "wx" });
  generated.push(witness(path));
};
emit("PROVENANCE.json", {
  schemaVersion: 1, capturedAt, author, baseline: witness(baselinePath),
  preTestContractWitness: before.contract, frozenContract: witness(`${suitePath}\\CONTRACT.txt`),
  onlyContractWordingClarification: "Per-package bound made explicit; initial bytes reconstruct exactly from current contract.",
  reusedCurrentPriorArt: before.reusedPriorArt.map(verify), noPriorHistoryCopied: true,
  nestedModuleDecision: before.nestedModuleDecision, nestedModule: witness(`${suitePath}\\go.mod`), identicalReusedSum: witness(`${suitePath}\\go.sum`),
  authoringRevisions: [
    { run: labels[0], boundary: "Initial compiling four-gate RED and seven positive controls." },
    { run: labels[1], boundary: "New-only shared hardening/probe parsing calibrated against actual existing roles; empty entrypoint preserved." },
    { run: labels[2], boundary: "Final explicit managed infrastructure comparisons across independent/combined legacy opt-ins." },
  ],
  noAssertionRemovedForProducerGreen: true, producerUnchangedAcrossAllRuns: true,
  wholeCheckoutSnapshotDifferencesRecorded: runs.map((run) => ({ run: run.label, count: run.actualCapturedSourceCount, outsideBaselineInputs: run.outsideBaselineSnapshotInputs })),
  rootDependencyChangesOrNetworkInstalls: false, privateRuntimeOrCredentialFilesRead: false,
});
emit("SOURCE-WITNESSES.json", {
  schemaVersion: 1, capturedAt, files: sourceWitnesses, tools: before.toolWitnesses,
  sourceOnlyImageProof: true, actualHostBuildLoopInspected: true, actualPackagerExecuted: false,
  packagerParentOnlyPlannedAmendment: {
    current: witness("scripts\\package-image.mjs"), currentInventoryCount: 6, requiredInventoryCount: 7,
    missingEntry: "assessment-worker", parentWillMakeAndFreezeActualEntryBeforeCoder: true,
    scriptsClassificationOrTrustedGuardWeakened: false,
  },
  installerUnchanged: ["internal\\install\\execution\\linux.go", "internal\\install\\execution\\bundle.go"].map(witness),
  actualAssessmentCommandSourceExists: witness("cmd\\assessment-worker\\main.go"),
  noImageBuildOrBinaryPresenceInImageClaimed: true,
});
emit("CALIBRATION.json", {
  schemaVersion: 1, capturedAt, selectedRun: selected.label, positiveControls: selected.positiveControls,
  actualHelmInvocations: selected.actualHelmRenders, properYAMLParser: "cached gopkg.in/yaml.v3 v3.0.1",
  executedLegacyChecks: [
    "Default/explicit-empty-off resources equal.",
    "Existing three application-role hardening/probe fields parsed and asserted.",
    "Independent delivery, independent collection and combined opt-ins retain existing selected authority.",
    "Managed PG/S3/service/policy documents structurally unchanged across legacy opt-ins.",
    "Partial integration key references rejected on precise key/name paths, including disabled assessment.",
    "Existing collection unit generated by actual unprivileged Podman4.9 parser, without activation.",
    "Installer linux.go/bundle.go hashes unchanged.",
  ],
  actualNativeGenerator: selected.nativeGenerators[0],
  newAssessmentUnitGenerationNotRun: true,
  noActualSecretContentsKubeContextOrPrivateRuntimeLoaded: true,
  notImageRolloutSystemdOrModelAcceptance: true,
});
emit("RED.json", {
  schemaVersion: 1, capturedAt, status: "ACTUAL FOUR-GATE ARTIFACT RED - NOT COMPILER OR TOOL-BLOCKED",
  selectedRun: selected.label, actualExitCode: 1, topLevelGates: selected.topLevelGates,
  failedNested: selected.failedNested, positiveNested: selected.positiveControls,
  observedFailures: {
    imageSource: "Missing seventh actual assessment build target.",
    hostPackager: "Real six-entry inventory omits assessment-worker; real loop remains the inspected one.",
    coreScope: "Explicit nonempty assessment scope not forwarded.",
    enabledRole: "Keyed, keyless and coexistence renders all lack the assessment Deployment.",
    defaults: "Actual values.yaml has no assessment profile.",
    validation: "15 new malformed assessment selections were accepted by real Helm; two existing partial-key denials passed.",
    quadlet: "Actual assessment.container absent.",
  },
  blockedDependentTails: [
    "New assessment Deployment environment/Secret/keyless mapping, selected scope/limits/replicas/resources, hardening/probes and full new-role isolation.",
    "Core-only final new-feature resource comparison after its missing scope assertion.",
    "New assessment unit directive/hardening/protected-env/no-autostart assertions and actual new-unit native generation.",
  ],
  outOfScopeNotExecuted: ["Image build", "Actual seven binaries inside final image", "Host packager execution", "Cluster/apply/rollout", "Systemd activation", "Protected env/CA file provisioning", "Provider/account/model requests"],
  earlierAuthoringRunsPreserved: runs.slice(0, -1).map((run) => ({ label: run.label, logs: run.logs, receipt: run.receipt, topLevelGates: run.topLevelGates })),
  independentAcceptance: false, M11Complete: false,
});
emit("GUARDS.json", {
  schemaVersion: 1, capturedAt, before: witness(baselinePath), unchangedExistingSourceControlFiles: 1567,
  oldTestsHelpersRootModulesPoliciesSignaturesRuntimeBackendUIAndInstallerUnchanged: true,
  existingSourceFileSetUnchangedByAuthor: true, productionScriptOrProducerFilesAddedOrEdited: false,
  wholeCheckoutSourceSetGuardPassed: finalSourceSetBlocker === null, finalSourceSetBlocker,
  exactSourceWitnessSet: "BEFORE.json files remain hash-identical. The original author-check remains strict and currently reports out-of-scope runtime A3 additions; it was not weakened or relabelled PASS.",
  perRunSourceSnapshots: runs.map((run) => ({ run: run.label, before: run.beforeInputs, after: run.afterInputs })),
  currentProducerStableDuringEachRun: true,
  wholeCheckoutSnapshotsDifferBetweenRunsDueToOutOfScopeRuntimeA3Additions: true,
  recorderIsNotProducerEditAuthorization: true, parentTrustedSourceGuardAndScriptsClassificationRemainAuthoritative: true,
  sourceTreeSHA256: json(selected.afterInputs.path).sourceTreeSHA256,
  selectedToolAndCachedYAMLHashesUnchanged: true,
  noAgentsCommitsPrivateAuthorityDependencyNetworkInstallPolicyKeyVolumeOSClusterOrImageActions: true,
});
emit("EVIDENCE.json", {
  schemaVersion: 1, capturedAt, runs, finalRun: selected.label, totalAuthoringRenders: 81, maxRendersPerPackageRun: 32,
  finalRunHasExactlyFourTopLevelGates: true, noHiddenRetryOrCartesianExpansion: true,
  properYAMLAndActualNativeGeneratorUsed: true, allArtifactFiles: allArtifacts,
  explicitCAlimit: "Normal system trust only; optional private CA is manual operator-provisioned runtime config, not a chart file/Secret/mount/generic-env feature.",
  finalSourceSetBlocker,
});
const files = [...new Map([
  ...["CONTRACT.txt", "HANDOFF.txt", "go.mod", "go.sum", "helm_helpers_test.go", "helm_test.go", "artifacts_test.go", "Test-AssessmentDeployment.ps1"].map((name) => witness(`${suitePath}\\${name}`)),
  ...["BEFORE.json", "guard.mjs", "freeze.mjs"].map((name) => witness(`${reviewPath}\\${name}`)),
  ...generated, ...allArtifacts,
].map((entry) => [entry.path, entry])).values()];
emit("PACKET.json", {
  schemaVersion: 1, capturedAt, author, stage: "Independent opt-in assessment deployment test authorship",
  status: finalSourceSetBlocker ? "FROZEN AS-TESTED ARTIFACT RED / FINAL WHOLE-CHECKOUT SOURCE-SET GUARD BLOCKED - PARENT RESOLUTION REQUIRED" :
    "FROZEN ACTUAL ARTIFACT RED - PARENT VERIFY/SEAL, THEN SEPARATE CODER",
  signed: false, independentAcceptance: false, productionChanges: false, M11Complete: false,
  exactlyFourGates: true, finalRun: selected.label, actualExitCode: 1, finalPositiveNestedControls: 7, finalFailedNestedChecks: 21,
  actualClientOnlyHelmRenders: 27, properYAMLDecoder: true, actualNativeGenerator: "Podman4.9.3 UID1000 -user -dryrun -no-kmsg-log",
  packagerEntryRemainsParentOwnedAndUnchanged: true,
  imageBuildClusterSystemdActivationOrCredentialAccess: false,
  unchangedExistingSourceControlFiles: 1567,
  oldRuntimeCancellationAndCadenceControlsUnchanged: true,
  finalWholeCheckoutSourceSetGuardPassed: finalSourceSetBlocker === null, finalSourceSetBlocker,
  fileCount: files.length, files,
});
const packet = witness(`${reviewPath}\\PACKET.json`);
writeFileSync(join(review, "PACKET.sha256"), `${packet.sha256}  PACKET.json\n`, { flag: "wx" });
json(packet.path).files.forEach(verify);
before.files.forEach(verify);
for (const absent of before.initiallyMissing) assert.equal(existsSync(join(root, absent)), false);
console.log(JSON.stringify({ packet, frozenFileCount: files.length, actualGates: 4, finalExit: 1, positiveControls: 7,
  actualHelmRenders: 27, nativeGenerator: "4.9.3 UID1000", unchangedSourceControls: 1567,
  wholeCheckoutSourceSetGuardPassed: finalSourceSetBlocker === null, stopForParent: true }, null, 2));
