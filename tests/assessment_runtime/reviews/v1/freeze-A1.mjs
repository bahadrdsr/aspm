import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const review = dirname(fileURLToPath(import.meta.url));
const root = resolve(review, "..", "..", "..", "..");
const reviewPath = "tests\\assessment_runtime\\reviews\\v1";
const artifactPath = ".artifacts\\assessment-runtime-v1";
const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const json = (path) => JSON.parse(readFileSync(join(root, path)));
const witness = (path) => {
  const bytes = readFileSync(join(root, path));
  return { path, bytes: bytes.length, sha256: hash(bytes) };
};
const verify = (entry) => {
  const actual = witness(entry.path);
  assert.equal(actual.sha256, entry.sha256, entry.path);
  if (entry.bytes !== undefined) assert.equal(actual.bytes, entry.bytes, entry.path);
  return actual;
};
const baselinePath = `${reviewPath}\\BEFORE-A1.json`;
assert.equal(witness(baselinePath).sha256, "0dfc6df61bea16d63f256b67629e33b951c38341776f022d9259b5b998d81552");
const baseline = json(baselinePath);
const helperPath = baseline.onlyAuthorizedExistingEdit.original.path;
const beforeHelper = baseline.onlyAuthorizedExistingEdit.original;
const afterHelper = witness(helperPath);
verify(baseline.onlyAuthorizedExistingEdit.archive);
execFileSync(process.execPath, [join(review, "prepare-A1.mjs"), "verify-cadence"], { cwd: root, stdio: "pipe" });
assert.notEqual(beforeHelper.sha256, afterHelper.sha256);
const unchanged = baseline.existingNonWebInputs.filter((entry) => entry.path !== helperPath).map(verify);
assert.equal(unchanged.length, 915);
const held = baseline.heldRuntimeSource7.map(verify);
const backend = baseline.frozenBackendControls.map(verify);
assert.equal(held.length, 7);
assert.equal(backend.length, 245);
const runtimeUnchanged = baseline.originalRuntimeControls66.filter((entry) => entry.path !== helperPath).map(verify);
assert.equal(runtimeUnchanged.length, 65);
verify(baseline.runtimeManifest);
verify(baseline.runtimeSignature);
verify(baseline.originalRuntimePacket);
verify(baseline.backendPacket);
const inherited = baseline.inheritedEvidence.map(verify);
const author = "independent-runtime-test-successor-20260921";
const prefix = "successor-runtime-a1-";
const artifactNames = readdirSync(join(root, artifactPath)).filter((name) => name.startsWith(prefix));
const receipts = artifactNames.filter((name) => name.endsWith(".receipt.json")).map((name) => `${artifactPath}\\${name}`);
assert.equal(receipts.length, 8);
const runEvidence = [];
const binaries = new Set();
const originalRuntimeHashes = new Map(baseline.existingNonWebInputs.map((entry) => [entry.path, entry.sha256]));
for (const path of receipts) {
  const receipt = json(path);
  assert.equal(receipt.allInputsStable, true, path);
  assert.equal(receipt.inputCount, 920, path);
  const input = json(receipt.inputs.path);
  verify(receipt.inputs);
  assert.equal(input.allStable, true);
  for (const entry of input.files) {
    if (!originalRuntimeHashes.has(entry.path)) continue;
    if (entry.path === helperPath) assert.ok([beforeHelper.sha256, afterHelper.sha256].includes(entry.sha256));
    else assert.equal(entry.sha256, originalRuntimeHashes.get(entry.path), entry.path);
  }
  verify(receipt.actualCommand);
  binaries.add(receipt.actualCommand.sha256);
  const stage = receipt.stages.find((stage) => stage.stage === "tests");
  assert.ok(stage);
  assert.ok(receipt.stages.every((stage) => !stage.hardTimeout && !stage.outputExceeded));
  const eventsPath = relative(root, stage.events);
  const events = readFileSync(join(root, eventsPath), "utf8").split(/\r?\n/).filter((line) => line.startsWith("{")).map(JSON.parse);
  const tests = events.filter((event) => event.Test && ["pass", "fail", "skip"].includes(event.Action))
    .map(({ Test, Action, Elapsed }) => ({ test: Test, action: Action, elapsedSeconds: Elapsed }));
  assert.ok(tests.every((test) => test.action !== "skip"));
  const final = events.findLast((event) => !event.Test && ["pass", "fail"].includes(event.Action));
  assert.ok(final);
  assert.equal(receipt.exitCode, final.Action === "pass" ? 0 : 1);
  runEvidence.push({
    run: path.split("\\").at(-1).replace(".receipt.json", ""), mode: receipt.mode, repeatCount: receipt.repeatCount,
    fixtureSHA256: input.files.find((entry) => entry.path === helperPath).sha256,
    diagnosticOnly: receipt.diagnosticIsNotNormalAcceptance, exitCode: receipt.exitCode,
    packageResult: final.Action, packageElapsedSeconds: final.Elapsed, tests,
    receipt: witness(path), inputs: verify(receipt.inputs), actualCommand: verify(receipt.actualCommand),
    stageArtifacts: receipt.stages.flatMap((stage) => [witness(relative(root, stage.events)), witness(relative(root, stage.log))]),
  });
}
assert.equal(binaries.size, 1, "All runs must use the identical actual held assessment-worker binary");
const normal = runEvidence.find((run) => run.run.includes("amended-contract"));
assert.equal(normal.packageResult, "pass");
assert.equal(normal.tests.filter((test) => !test.test.includes("/")).length, 4);
assert.ok(normal.tests.every((test) => test.action === "pass"));
assert.equal(normal.fixtureSHA256, afterHelper.sha256);
const original = runEvidence.find((run) => run.run.includes("original-ar4"));
assert.equal(original.fixtureSHA256, beforeHelper.sha256);
assert.equal(original.tests.find((test) => test.test.endsWith("/actual-command-crash")).action, "fail");
const repeated = runEvidence.find((run) => run.run.includes("unchanged-context"));
assert.deepEqual(repeated.tests.filter((test) => test.test.endsWith("/Run-context")).map((test) => test.action), ["fail", "pass", "pass"]);
const observations = artifactNames.filter((name) => name.endsWith(".observation.json")).map((name) => {
  const path = `${artifactPath}\\${name}`;
  const data = json(path);
  assert.equal(data.eventOverflow, false);
  assert.equal(data.readonlySQL, true);
  assert.equal(data.businessWrites, false);
  assert.ok(data.childCleanup.every((child) => child.exited && child.killCalled));
  return { witness: witness(path), data };
});
assert.equal(observations.length, 13);
const originalCrashes = observations.filter(({ data }) => data.kind === "actual-command-crash" && data.failed);
assert.equal(originalCrashes.length, 2);
const crashedProof = originalCrashes.map(({ witness, data }) => {
  const last = data.samples.findLast((sample) => sample.stage === "body-return-before-cleanup");
  assert.equal(data.totalProtectedAPICalls, 221);
  assert.equal(data.nativePOSTs, 1);
  assert.equal(last.rows[0].state, "uncertain");
  assert.equal(last.rows[0].attempts, 1);
  assert.equal(last.rows[0].ioReleasedAt, null);
  assert.ok(last.rows[0].ioDeadlineRemainingMillis > 0);
  assert.equal(last.rows[1].state, "queued");
  assert.equal(last.rows[1].attempts, 0);
  return { witness, attemptedAPICalls: 221, actualHTTPBudget: 220, nativePOSTs: 1, remainingReservationMillis: last.rows[0].ioDeadlineRemainingMillis, childCleanup: data.childCleanup };
});
const amendedCrash = observations.find(({ witness }) => witness.path.includes("amended-crash-"));
assert.ok(amendedCrash);
assert.equal(amendedCrash.data.failed, false);
assert.equal(amendedCrash.data.nativePOSTs, 2);
assert.equal(amendedCrash.data.totalProtectedAPICalls, 67);
const finalRows = amendedCrash.data.samples.find((sample) => sample.stage === "completed-tail").rows;
assert.equal(finalRows[0].state, "uncertain");
assert.equal(finalRows[0].attempts, 1);
assert.equal(finalRows[0].ioReleasedAt, null);
assert.equal(finalRows[1].state, "succeeded");
assert.equal(finalRows[1].attempts, 1);
const microseconds = (value) => {
  const match = value.match(/\.(\d+)(?:Z|[+-]\d\d:\d\d)$/);
  return BigInt(Date.parse(value)) * 1000n + BigInt((match?.[1] ?? "").padEnd(6, "0").slice(3, 6));
};
const delayAfterDeadlineMicros = microseconds(finalRows[1].dispatchStartedAt) - microseconds(finalRows[0].ioDeadline);
assert.ok(delayAfterDeadlineMicros >= 0n);
const contextObservations = observations.filter(({ data }) => data.kind === "Run-context");
assert.equal(contextObservations.length, 10);
const competing = contextObservations.filter(({ data }) => data.events.some((event) =>
  event.event === "before-native-cancel-event" && event.heldCancelled && event.sharedContextDone && event.sharedDeadlineRemainingMillis < 0));
assert.equal(competing.length, 1);
const timeline = competing[0].data.events;
const event = (name) => timeline.find((event) => event.event === name);
assert.ok(event("actual-local-native-conn-Close-returned").elapsedMillis < event("after-stopRole-and-listener-check").elapsedMillis);
assert.ok(event("native-request-context-done").sharedDeadlineRemainingMillis > 0);
assert.equal(event("before-native-cancel-event").heldCancelled, true);
assert.ok(contextObservations.every(({ data }) => !data.failed), "Report any instrumented failure explicitly instead of silently updating this expectation");
const toolFailurePath = `${artifactPath}\\successor-runtime-a1-original-diagnostic-20260921-01.execution-note.json`;
assert.equal(json(toolFailurePath).runnerReceiptCompleted, false);
const diagnostics = artifactNames.filter((name) => name.endsWith(".diagnostic-inputs.json")).map((name) => {
  const path = `${artifactPath}\\${name}`;
  const manifest = json(path);
  assert.equal(manifest.reversibleAssertionPreservationVerified, true);
  assert.equal(manifest.typedProductionBindingCopiedByteExact, true);
  for (const entry of manifest.copies) assert.equal(existsSync(join(root, entry.copy)), false);
  assert.equal(existsSync(join(root, ".cache", "assessment-runtime-a1", manifest.label)), false);
  return witness(path);
});
const walkGo = (dir) => readdirSync(join(root, dir), { withFileTypes: true }).flatMap((entry) => {
  const path = join(dir, entry.name);
  return entry.isDirectory() ? walkGo(path) : entry.name.endsWith(".go") ? [path] : [];
});
assert.deepEqual([...walkGo("internal"), ...walkGo("cmd")].sort(), [...baseline.productionGoFileSet].sort());
const runtimeFile = process.argv[2];
assert.ok(runtimeFile, "Explicit selected private runtime required for in-memory canary check");
const selected = JSON.parse(readFileSync(runtimeFile, "utf8"));
const db = new URL(selected.databaseUrl);
assert.equal(db.hostname, "127.0.0.1");
assert.equal(db.port, "15432");
const canaries = [...new Set([selected.databaseUrl, selected.s3AccessKey, selected.s3SecretKey, selected.postgresPassword, decodeURIComponent(db.password)]
  .filter(Boolean).flatMap((value) => [value, encodeURIComponent(value), JSON.stringify(value).slice(1, -1)]))];
const artifacts = artifactNames.map((name) => witness(`${artifactPath}\\${name}`));
for (const entry of artifacts.filter((entry) => !entry.path.endsWith(".exe"))) {
  const text = readFileSync(join(root, entry.path), "utf8");
  assert.ok(canaries.every((value) => !text.includes(value)), "Selected private runtime canary leaked; values withheld");
}
const generated = [];
const capturedAt = new Date().toISOString();
function emit(name, value) {
  const path = `${reviewPath}\\${name}`;
  assert.equal(existsSync(join(root, path)), false, `Refusing to overwrite ${name}`);
  writeFileSync(join(root, path), JSON.stringify(value, null, 2) + "\n", { flag: "wx" });
  const entry = witness(path);
  generated.push(entry);
  return entry;
}
emit("CALIBRATION-A1.json", {
  schemaVersion: 1, capturedAt, author, originalCrashProof: crashedProof,
  approvedCadence: { beforeMillis: 20, afterMillis: 100, otherWaitMillis: 20, totalAPIBudget: 220, awaitSeconds: 8, fixtureSeconds: 90, workerRequestSeconds: 5, requestWindowSeconds: 3, leaseSeconds: 1 },
  exactFixtureDelta: { original: beforeHelper, amended: afterHelper, archive: verify(baseline.onlyAuthorizedExistingEdit.archive), reconstructionVerifier: witness(`${reviewPath}\\prepare-A1.mjs`) },
  amendedCrash: { observation: amendedCrash.witness, counter: 67, nativePOSTs: 2, oldUncertainAttemptRetained: true, oldIOReleaseAcknowledgment: null, newDispatchAfterOldDeadlineMicros: Number(delayAfterDeadlineMicros),
    executedTails: ["actual fresh command succeeded", "old attempt not resent and result absent", "configured quota unchanged", "durable dispatch history retained", "domain/storage unchanged", "both exact owned command PIDs exited"],
    childCleanup: amendedCrash.data.childCleanup },
  normalFour: normal, passingNormalSuiteDoesNotResolveCancellationFlake: true,
  diagnosticsPreserveEveryOriginalAssertion: true, diagnosticCopies: diagnostics, diagnosticOnlyNotNormalAcceptance: true,
});
emit("CANCELLATION-A1.json", {
  schemaVersion: 1, capturedAt, status: "OPEN QUALIFICATION - NO CANCELLATION TEST OR PRODUCTION AMENDMENT AUTHORIZED",
  inheritedFailure: baseline.inheritedEvidence.filter((entry) => entry.path.includes("contract-20260921-01")),
  independentUnchangedReproduction: repeated,
  observedCompetingReadyWindow: { evidence: competing[0].witness, beforeStop: event("before-stopRole"), actualClose: event("actual-local-native-conn-Close-returned"),
    nativeContextDone: event("native-request-context-done"), afterStopAndListenerCheck: event("after-stopRole-and-listener-check"), beforeEvent: event("before-native-cancel-event"),
    selectedNativeCaseAndPassed: true },
  observations: contextObservations.map(({ witness, data }) => ({ witness, failed: data.failed, nativePOSTs: data.nativePOSTs, apiCounter: data.totalProtectedAPICalls })),
  established: ["An unchanged normal Run-context failure was independently reproduced.", "In one diagnostic, native socket/context ended promptly but stopRole returned after the shared five-second context expired.", "The unchanged event helper entered with both its channels already ready."],
  inference: "The unprioritized select can report a missing cancellation despite timely native cancellation when both channels are ready. This mechanism was observed, but no failing instrumented run tied every historical failure to it.",
  languageAuthority: "Go1.27 language specification, Select statements, multiple-ready-case selection; consulted official Go specification on 2026-09-21.",
  notEstablished: ["Cause of the long stopRole delay.", "Definitive attribution of each uninstrumented/historical failure.", "A production cancellation defect.", "Resolution from repeated passing trials."],
  boundedTraceLimit: "Final six diagnostic repeats included read-only stack-function-label capture on shared deadline; none hit that deadline, so no delay-attribution stack labels were collected.",
  noEventAssertionDeadlineOrCancelDelayChanged: true, separateParentApprovalRequiredForAnyMeasurementAmendment: true, furtherRetriesStopped: true,
});
emit("RED-A1.json", {
  schemaVersion: 1, capturedAt, inheritedEvidence: inherited, independentOriginalAR4: original, independentUnchangedContextRepeats: repeated,
  crashMeasurementRED: crashedProof, correctedCadenceNormalSuite: normal,
  incompleteDiagnosticToolFailure: witness(toolFailurePath),
  blockedTails: {
    originalCrash: ["New explicit job completion", "new-job final no-repeat/domain assertions"],
    unchangedContextFailure: ["fresh actual command startup", "prior-job reopened receipt and quota assertions", "new explicit job and final domain assertions"],
  },
  afterAmendmentReachedTails: ["normal AR1-AR4 assertions including both AR4 subcases", "amended diagnostic crash admission after unreleased old reservation deadline"],
  preserveUnconditionalTopLevelLogQualification: "AR4 emits a completion log even when nested tests fail; only actual test events and reached assertions classify outcomes.",
  cancellationQualificationStillOpen: true, runtimeAccepted: false, M11Complete: false,
});
emit("GUARDS-A1.json", {
  schemaVersion: 1, capturedAt, baseline: witness(baselinePath), heldRuntimeSource7: held, handoff: verify(baseline.heldHandoff),
  totalExistingNonWebInputs: 916, unchangedInputs: 915, intentionalChangedInput: { before: beforeHelper, after: afterHelper, exactReconstructionVerified: true },
  originalRuntimeControls: 66, unchangedOriginalRuntimeControls: 65, intentionalRuntimeControlChanges: 1,
  originalRuntimeManifest: verify(baseline.runtimeManifest), originalRuntimeSignature: verify(baseline.runtimeSignature),
  originalSignatureNowHistoricalForAmendedHelper: true, publicOracleConfigSHA256: baseline.runtimePublicOracleConfigSHA256,
  originalRuntimePacket: verify(baseline.originalRuntimePacket), frozenBackendControlsUnchanged: 245, frozenBackendPacket: verify(baseline.backendPacket),
  currentProductionSourceCapturedRatherThanHistoricalPreFixSource: true, productionGoFileSetUnchanged: baseline.productionGoFileSet.length,
  productionCodeShadowsAdded: false, diagnosticCopiesRemoved: true, actualCommandBinarySHA256: [...binaries][0],
  allCompleteRunReceiptsStable: true, redactionCanaryScanPassed: true, excludedPaths: ["web\\**", "README.md"],
  noAgentsCommitsDependenciesProductionOracleAuthorityOSClusterPolicyVolumeOrUIChanges: true,
});
emit("EVIDENCE-A1.json", {
  schemaVersion: 1, capturedAt, author, notOriginalRuntimeAuthor: true, baseline: witness(baselinePath),
  independentRuns: runEvidence, readonlyObservations: observations.map(({ witness }) => witness),
  diagnosticCopies: diagnostics, incompleteDiagnosticToolFailure: witness(toolFailurePath),
  priorCoderCompatibilityEvidenceNotReclassifiedAsSuccessorRuns: true, allRunArtifacts: artifacts,
  evidenceLimit: "Normal suite passed once after polling amendment; cancellation failure qualification remains open. No production cancellation fix or measurement change claimed.",
});
const fixed = ["BEFORE-A1.json", "AMENDMENT-A1.txt", "HANDOFF-A1.txt", "Test-Runtime-A1.ps1", "prepare-A1.mjs", "observe-A1.go.txt", "freeze-A1.mjs",
  "history\\a1\\fixtures_test.go.txt"].map((name) => witness(`${reviewPath}\\${name}`));
const files = [...new Map([...fixed, afterHelper, ...generated, ...artifacts].map((entry) => [entry.path, entry])).values()];
emit("PACKET-A1.json", {
  schemaVersion: 1, capturedAt, author, status: "FROZEN SUCCESSOR CADENCE AMENDMENT / CANCELLATION QUALIFICATION OPEN - PARENT VERIFY AND SEAL",
  signed: false, runtimeAccepted: false, M11Complete: false, productionEdits: false,
  onlyExistingEdit: helperPath, originalRuntimePacketSHA256: baseline.originalRuntimePacket.sha256,
  frozenBackendA2PacketSHA256: baseline.backendPacket.sha256, heldRuntimeHandoffSHA256: baseline.heldHandoff.sha256,
  heldRuntimePaths: 7, oldRuntimeControlsUnchanged: 65, approvedAmendedRuntimeControls: 1, frozenBackendControlsUnchanged: 245,
  actualNormalFourResult: "PASS 18.294s", cancellationQualification: "OPEN; preserve the independent unchanged failure and observed competing-ready measurement window.",
  owner: "Parent verifies evidence/new seal and decides any further cancellation measurement amendment before coder finalization.",
  fileCount: files.length, files,
});
const packet = witness(`${reviewPath}\\PACKET-A1.json`);
writeFileSync(join(review, "PACKET-A1.sha256"), `${packet.sha256}  PACKET-A1.json\n`, { flag: "wx" });
json(packet.path).files.forEach(verify);
unchanged.forEach(verify);
held.forEach(verify);
console.log(JSON.stringify({ packet, frozenFileCount: files.length, heldRuntimeUnchanged: held.length, originalRuntimeUnchanged: 65, approvedHelperChanges: 1, backendControlsUnchanged: backend.length, normalFour: "PASS", cancellationQualification: "OPEN", stopForParent: true }, null, 2));
