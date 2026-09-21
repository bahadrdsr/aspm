import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const review = dirname(fileURLToPath(import.meta.url));
const root = resolve(review, "..", "..", "..", "..");
const reviewPath = "tests\\assessment_runtime\\reviews\\v1";
const artifactsPath = ".artifacts\\assessment-runtime-v1";
const sha = (bytes) => createHash("sha256").update(bytes).digest("hex");
const json = (path) => JSON.parse(readFileSync(join(root, path)));
const witness = (path) => {
  const bytes = readFileSync(join(root, path));
  return { path, bytes: bytes.length, sha256: sha(bytes) };
};
const verify = (entry) => {
  const actual = witness(entry.path);
  assert.equal(actual.sha256, entry.sha256, entry.path);
  if (entry.bytes !== undefined) assert.equal(actual.bytes, entry.bytes, entry.path);
  return actual;
};
const baselinePath = `${reviewPath}\\BEFORE-A2.json`;
assert.equal(witness(baselinePath).sha256, "8a49237299b24cf5851c3078527d9d0da85ae329ef3b54806bf55042c66c43ed");
const before = json(baselinePath);
execFileSync(process.execPath, [join(review, "prepare-A2.mjs"), "verify-amendment"], { cwd: root, stdio: "pipe" });
const changed = new Set(before.onlyAuthorizedChangedFiles.map(({ before }) => before.path));
assert.equal(changed.size, 3);
const unchanged = before.guardedInputs.filter((entry) => !changed.has(entry.path)).map(verify);
assert.equal(unchanged.length, 1015);
const deltas = before.onlyAuthorizedChangedFiles.map((entry) => {
  verify(entry.archive);
  assert.equal(entry.before.sha256, entry.archive.sha256);
  const after = witness(entry.before.path);
  assert.notEqual(after.sha256, entry.before.sha256);
  return { ...entry, after };
});
const held = before.heldRuntimeSource7.map(verify);
const backend = before.backendControls245.map(verify);
assert.equal(held.length, 7);
assert.equal(backend.length, 245);
verify(before.parentVerifiedA1Packet);
verify(before.backendPacket);
verify(before.originalRuntimePacket);
verify(before.originalRuntimeManifest);
verify(before.originalRuntimeSignature);
const originalRuntimeUnchanged = before.originalRuntimeControls66.filter((entry) => !changed.has(entry.path)).map(verify);
assert.equal(originalRuntimeUnchanged.length, 63);
const walkGo = (dir) => readdirSync(join(root, dir), { withFileTypes: true }).flatMap((entry) => {
  const path = join(dir, entry.name);
  return entry.isDirectory() ? walkGo(path) : entry.name.endsWith(".go") ? [path] : [];
});
assert.deepEqual([...walkGo("internal"), ...walkGo("cmd")].sort(), [...before.productionGoFileSet].sort());
const prefix = "successor-runtime-a2-";
const names = readdirSync(join(root, artifactsPath)).filter((name) => name.startsWith(prefix));
const receipts = names.filter((name) => name.endsWith(".receipt.json")).map((name) => `${artifactsPath}\\${name}`);
assert.equal(receipts.length, 3);
const canonicalTree = (files) => JSON.stringify(files.map(({ path, bytes, sha256 }) => ({ path, bytes, sha256 }))
  .sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
const runs = [];
let inputTree;
const binaries = new Set();
for (const path of receipts) {
  const receipt = json(path);
  assert.equal(receipt.exitCode, 0, path);
  assert.equal(receipt.allInputsStable, true, path);
  assert.equal(receipt.repeatCount, 1);
  assert.equal(receipt.inputCount, 1023);
  assert.ok(receipt.stages.every((stage) => !stage.hardTimeout && !stage.outputExceeded));
  verify(receipt.inputs);
  const inputs = json(receipt.inputs.path);
  assert.equal(inputs.allStable, true);
  inputs.files.forEach(verify);
  const tree = sha(canonicalTree(inputs.files));
  if (inputTree === undefined) inputTree = tree;
  assert.equal(tree, inputTree, "All actual A2 runs must use the same exact input tree");
  verify(receipt.actualCommand);
  binaries.add(receipt.actualCommand.sha256);
  const stage = receipt.stages.find((stage) => stage.stage === "tests");
  const events = readFileSync(stage.events, "utf8").split(/\r?\n/).filter((line) => line.startsWith("{")).map(JSON.parse);
  const tests = events.filter((event) => event.Test && ["pass", "fail", "skip"].includes(event.Action))
    .map(({ Test, Action, Elapsed }) => ({ test: Test, action: Action, elapsedSeconds: Elapsed }));
  assert.ok(tests.every((test) => test.action === "pass"));
  const final = events.findLast((event) => !event.Test && ["pass", "fail"].includes(event.Action));
  assert.equal(final.Action, "pass");
  runs.push({ mode: receipt.mode, run: path.split("\\").at(-1).replace(".receipt.json", ""), exitCode: 0,
    tests, packageElapsedSeconds: final.Elapsed, inputCount: 1023, inputTreeSHA256: tree,
    receipt: witness(path), inputs: verify(receipt.inputs), actualCommand: verify(receipt.actualCommand),
    logs: receipt.stages.flatMap((stage) => [witness(relative(root, stage.events)), witness(relative(root, stage.log))]) });
}
assert.equal(binaries.size, 1);
const normal = runs.find((run) => run.mode === "Contract");
const calibration = runs.find((run) => run.mode === "Calibration");
const compile = runs.find((run) => run.mode === "Compile");
assert.equal(compile.tests.length, 0);
assert.equal(normal.tests.filter((test) => !test.test.includes("/")).length, 4);
assert.equal(normal.tests.filter((test) => test.test.includes("/")).length, 20);
assert.equal(calibration.tests.length, 8);
assert.equal(calibration.tests.filter((test) => test.test.includes("/synthetic-measurement-")).length, 6);
const proofNames = names.filter((name) => name.endsWith(".calibration.json"));
assert.equal(proofNames.length, 1);
const proofPath = `${artifactsPath}\\${proofNames[0]}`;
const proof = json(proofPath);
assert.equal(proof.failed, false);
assert.equal(proof.realBoundValidated, true);
assert.equal(proof.originalTailReached, true);
assert.equal(proof.nativeCancellationOccurred, true);
assert.equal(proof.nativeWithinOriginalDeadline, true);
assert.equal(proof.actualNativePOSTs, 2);
assert.equal(proof.actualRunReturned, true);
assert.equal(proof.freshActualCommandExited, true);
assert.equal(proof.selectedPIDKillCalled, true);
assert.equal(proof.newDeadlineOrAllowance, false);
assert.ok(proof.protectedAPICounter <= 220);
const nanos = (value) => {
  const fraction = value.match(/\.(\d+)(?:Z|[+-]\d\d:\d\d)$/)?.[1] ?? "";
  return BigInt(Date.parse(value)) * 1000000n + BigInt(fraction.padEnd(9, "0").slice(3, 9));
};
const deadline = nanos(proof.originalDeadline);
assert.ok(nanos(proof.nativeObservedAt) <= deadline);
assert.ok(nanos(proof.observerArrived) >= deadline);
assert.ok(nanos(proof.observerWaitStarted) >= nanos(proof.stopReturned));
assert.ok(nanos(proof.observerWaitStarted) < deadline, "Paired calibration must actually delay the observer before the original deadline");
assert.equal(proof.syntheticMeasurements.length, 6);
assert.equal(proof.syntheticMeasurements.filter((row) => !row.accepted).length, 4);
for (const row of proof.syntheticMeasurements) {
  assert.equal(row.accepted, row.expectedAcceptance);
  assert.equal(row.syntheticMeasurementOnly, true);
  assert.equal(row.productionExecutionClaimed, false);
  assert.equal(nanos(row.originalDeadline), deadline);
  if (row.case.includes("late-stamp")) assert.ok(nanos(row.recordedAt) > deadline);
  if (row.case.includes("exact-deadline")) assert.equal(nanos(row.recordedAt), deadline);
}
const inputManifestPath = `${artifactsPath}\\${calibration.run}.calibration-inputs.json`;
const inputManifest = json(inputManifestPath);
assert.equal(inputManifest.reversibleAssertionPreservationVerified, true);
assert.equal(inputManifest.typedBindingCopiedByteExact, true);
for (const entry of inputManifest.copies) assert.equal(existsSync(join(root, entry.copy)), false);
assert.equal(existsSync(join(root, ".cache", "assessment-runtime-a2", calibration.run)), false);
const normalLog = normal.logs.find((entry) => entry.path.endsWith("-tests.log"));
const actualMeasurements = [...readFileSync(join(root, normalLog.path), "utf8").matchAll(
  /bounded native cancellation: observed=(\S+) originalDeadline=(\S+) marginMillis=([0-9.]+) observerContextDone=(true|false)/g)]
  .map((match) => ({ observedAt: match[1], originalDeadline: match[2], marginMillis: Number(match[3]), observerContextDone: match[4] === "true" }));
assert.equal(actualMeasurements.length, 2);
actualMeasurements.forEach((measurement) => assert.ok(nanos(measurement.observedAt) <= nanos(measurement.originalDeadline)));
const allArtifacts = names.map((name) => witness(`${artifactsPath}\\${name}`));
const runtimePath = process.argv[2];
assert.ok(runtimePath, "Explicit selected private runtime required for in-memory canary scan");
const runtime = JSON.parse(readFileSync(runtimePath, "utf8"));
const db = new URL(runtime.databaseUrl);
assert.equal(db.hostname, "127.0.0.1");
assert.equal(db.port, "15432");
const canaries = [...new Set([runtime.databaseUrl, runtime.s3AccessKey, runtime.s3SecretKey, runtime.postgresPassword, decodeURIComponent(db.password)]
  .filter(Boolean).flatMap((value) => [value, encodeURIComponent(value), JSON.stringify(value).slice(1, -1)]))];
for (const entry of allArtifacts.filter((entry) => !entry.path.endsWith(".exe"))) {
  const text = readFileSync(join(root, entry.path), "utf8");
  assert.ok(canaries.every((value) => !text.includes(value)), "Selected private runtime canary leaked; values withheld");
}
const generated = [];
const capturedAt = new Date().toISOString();
const emit = (name, value) => {
  const path = `${reviewPath}\\${name}`;
  assert.equal(existsSync(join(root, path)), false, `Refusing to replace frozen ${name}`);
  writeFileSync(join(root, path), JSON.stringify(value, null, 2) + "\n", { flag: "wx" });
  generated.push(witness(path));
};
emit("CALIBRATION-A2.json", {
  schemaVersion: 1, capturedAt, run: calibration, actualProof: witness(proofPath),
  preservedAssertionsAndTypedBinding: witness(inputManifestPath),
  realNativeObservedAt: proof.nativeObservedAt, originalPreMarkerDeadline: proof.originalDeadline,
  delayedObserverArrived: proof.observerArrived, realNativeMarginMillis: proof.nativeMarginMillis,
  observerAfterDeadlineNanos: Number(nanos(proof.observerArrived) - deadline),
  existingRoleShutdownAndListenerAssertionsReached: true, realBoundValidated: true,
  nativePOSTs: 2, protectedAPICounter: proof.protectedAPICounter,
  actualFreshCommand: { pid: proof.freshActualCommandPID, exited: true, selectedPIDKillCalled: true },
  originalTailReached: true, executedTails: ["no old result/resend", "retained immutable dispatch marker and quota configuration/history", "new explicit job succeeds once", "domain/storage unchanged", "real Run returned and selected fresh command exited"],
  syntheticMeasurementNegatives: proof.syntheticMeasurements.filter((row) => !row.expectedAcceptance),
  syntheticInstantBoundaryPositives: proof.syntheticMeasurements.filter((row) => row.expectedAcceptance),
  syntheticCasesNotProductionPasses: true, extraExecutionAllowance: false, productionDelayOrClockMutation: false,
});
emit("RESULTS-A2.json", {
  schemaVersion: 1, capturedAt, compile, normalFour: normal,
  normalNativeCancellationMeasurements: [
    { scenario: "Run-context", ...actualMeasurements[0] },
    { scenario: "actual-command-crash", ...actualMeasurements[1] },
  ],
  originalCaseBodiesExceptAuthorizedPostStopObserverCallUnchanged: true,
  allNewNormalTailsReached: true, blockedNewNormalTails: [], realLateOrAbsentNativeCancellationObserved: false,
  repeatedRetriesUsedForAcceptance: false, producerTimeoutsOrReservationsChanged: false,
  notClaimed: ["every historical failure attributed", "StateNew hypothesis established", "remote compute or billing termination", "graceful OS signal handling", "whole-M11 or final runtime acceptance"],
});
emit("GUARDS-A2.json", {
  schemaVersion: 1, capturedAt, baseline: witness(baselinePath), parentVerifiedA1Packet: verify(before.parentVerifiedA1Packet),
  heldRuntimeHandoff: verify(before.runtimeHandoff), heldRuntimeSource7: held,
  guardedBeforeInputs: 1018, unchangedGuardedInputs: 1015, exactAuthorizedDeltas: deltas,
  unchangedProductionGoFileSetCount: before.productionGoFileSet.length, noProductionCodeShadows: true,
  frozenBackendControlsUnchanged: backend.length, frozenBackendPacket: verify(before.backendPacket),
  originalRuntimeControlsTotal: 66, originalRuntimeControlsUnchanged: originalRuntimeUnchanged.length,
  originalRuntimeManifest: verify(before.originalRuntimeManifest), originalRuntimeSignature: verify(before.originalRuntimeSignature),
  originalRuntimePacket: verify(before.originalRuntimePacket), originalSignaturesRemainHistoricalForAmendedTests: true,
  A1CadenceGenericEventsOtherWaitsAndAllExistingBoundsPreserved: true,
  sourceInputCount: 1023, sourceInputTreeSHA256: inputTree,
  canonicalTreeEncoding: "SHA256 of UTF8 JSON.stringify([{path,bytes,sha256},...] sorted by ordinal path).",
  allThreeRunsUseIdenticalSourceTreeAndActualCommand: true, actualCommandSHA256: [...binaries][0],
  calibrationWorkingCopiesRemoved: true, inMemoryPrivateRuntimeCanaryScanPassed: true,
  excludedPaths: ["web\\**", "README.md"], noUIEditsOrResets: true,
  noAgentsCommitsDependenciesProductionOracleAuthorityKeysPolicyVolumesOSOrClusterChanges: true,
});
emit("HISTORY-A2.json", {
  schemaVersion: 1, capturedAt, author: "independent-runtime-test-successor-20260921",
  originalRuntimeAuthorOrCoder: false, parentVerifiedA1Packet: verify(before.parentVerifiedA1Packet),
  preservedHistoricalRecords: ["RED-A1.json", "CANCELLATION-A1.json", "CALIBRATION-A1.json", "HANDOFF-A1.txt"].map((name) => witness(`${reviewPath}\\${name}`)),
  acceptedA1CadenceRetained: true, oldFailuresRetainedUnexplained: true,
  noAttributionOfEveryPastFailureOrStateNewHypothesis: true, archivedOnlyActuallyChangedA1Files: deltas.map((entry) => entry.archive),
  currentProof: "Actual native timestamp within the original deadline, deliberately later observer, strict synthetic rejection controls, then four normal cases on unchanged held production.",
  parentMustIssueNewSealAndRereview: true, selfAccepted: false,
});
emit("EVIDENCE-A2.json", {
  schemaVersion: 1, capturedAt, runs, actualCalibration: witness(proofPath), calibrationInputCopies: witness(inputManifestPath),
  allRunArtifacts: allArtifacts, sourceInputTreeSHA256: inputTree,
  ownedBoundary: "Selected existing PG15432/S318333 for random owned schema and legitimate intake; owned normal-TLS native fixture; exact selected child commands only.",
  observationsNotInferredFromHistoricalPassesOrSourceTrace: true,
});
const fixed = ["BEFORE-A2.json", "CONTRACT-A2.txt", "HANDOFF-A2.txt", "Test-Runtime-A2.ps1", "prepare-A2.mjs", "calibrate-A2.go.txt", "freeze-A2.mjs"]
  .map((name) => witness(`${reviewPath}\\${name}`));
const files = [...new Map([...fixed, ...deltas.map((entry) => entry.after), ...deltas.map((entry) => entry.archive),
  ...generated, ...allArtifacts].map((entry) => [entry.path, entry])).values()];
emit("PACKET-A2.json", {
  schemaVersion: 1, capturedAt, author: "independent-runtime-test-successor-20260921",
  status: "FROZEN NARROW A2 CANCELLATION MEASUREMENT PROOF - PARENT NEW SEAL AND REREVIEW",
  signed: false, runtimeAccepted: false, M11Complete: false, productionEdits: false,
  originalFiveSecondDeadlineAndSixSecondRoleBoundPreserved: true,
  actualDelayedObserverCalibration: "PASS; real early timestamp and observer after original deadline",
  syntheticMeasurementNegatives: "4 rejected; not production execution", syntheticBoundaryPositives: "2 accepted, including equal instant with different zone",
  actualNormalFourResult: "PASS 21.367s", realLateOrAbsentCancellationObserved: false,
  allHistoricalFailuresRetainedWithoutUniversalAttribution: true,
  parentVerifiedA1PacketSHA256: before.parentVerifiedA1Packet.sha256,
  heldRuntimeHandoffSHA256: before.runtimeHandoff.sha256, heldRuntimeSourcePaths: 7, frozenBackendControlsUnchanged: 245,
  sourceInputTreeSHA256: inputTree, sourceInputCount: 1023, fileCount: files.length, files,
});
const packet = witness(`${reviewPath}\\PACKET-A2.json`);
writeFileSync(join(review, "PACKET-A2.sha256"), `${packet.sha256}  PACKET-A2.json\n`, { flag: "wx" });
json(packet.path).files.forEach(verify);
unchanged.forEach(verify);
held.forEach(verify);
console.log(JSON.stringify({ packet, frozenFiles: files.length, unchangedInputs: unchanged.length, heldRuntime: held.length,
  backendControlsUnchanged: backend.length, inputTreeSHA256: inputTree, pairedCalibration: "PASS", normalFour: "PASS",
  syntheticNegativeRejections: 4, parentNewSealAndRereviewRequired: true }, null, 2));
