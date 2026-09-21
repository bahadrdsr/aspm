import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const review = dirname(fileURLToPath(import.meta.url));
const root = resolve(review, "..", "..", "..", "..");
const reviewPath = "tests\\ai_assessments\\reviews\\v1";
const sha = (bytes) => createHash("sha256").update(bytes).digest("hex");
const json = (path) => JSON.parse(readFileSync(join(root, path)));
const witness = (path) => {
  const bytes = readFileSync(join(root, path));
  return { path, bytes: bytes.length, sha256: sha(bytes) };
};
const verify = (entry, path = entry.path) => {
  const current = witness(path);
  assert.equal(current.sha256, entry.sha256, `Preservation failure: ${path}`);
  if (entry.bytes !== undefined) assert.equal(current.bytes, entry.bytes, path);
  return current;
};
const baselinePath = `${reviewPath}\\BEFORE-A2-SUCCESSOR.json`;
assert.equal(witness(baselinePath).sha256, "658540dad40798d62e8c6c13d3a108613732c6df115945b61d26c3b993857e7e");
const baseline = json(baselinePath);
const author = baseline.author;
const held = baseline.heldSource19.map((entry) => verify(entry));
const existing = baseline.backendAndExistingControls.map((entry) => verify(entry));
const controls = baseline.oldA1Controls.files.map((entry) => verify(entry));
const runtimeFiles = baseline.runtimeStageFiles.map((entry) => verify(entry));
assert.equal(held.length, 19);
assert.equal(controls.length, 221);
assert.equal(existing.length, 882);
assert.ok(existing.every((entry) => !entry.path.startsWith("web\\") && entry.path !== "README.md"));
verify(baseline.oldA1Controls.manifest);
verify(baseline.oldA1Controls.signature);
const inheritedRuns = baseline.inheritedUnsealedRuns.map((entry) => verify(entry));
const archives = baseline.archivedInterruptedDrafts.map((entry) => {
  const relocated = entry.archive.path.endsWith(".go") ? `${entry.archive.path}.txt` : entry.archive.path;
  return {
    predecessorSource: entry.original,
    recordedArchiveAtSuccessorStart: entry.archive,
    finalArchive: verify(entry.archive, relocated),
    nameOnlyRelocation: relocated !== entry.archive.path,
    reason: relocated !== entry.archive.path ? "Preserve exact bytes without creating another Go integration package" : "Unchanged archive",
    successorSource: witness(entry.original.path),
  };
});
for (const name of ["BEFORE-A2.json", "HPACK-TABLES-A2.json", "calibrate-http2-A2.go.txt"]) {
  verify(baseline.inheritedDrafts.find((entry) => entry.path.endsWith(`\\${name}`)));
}
const pinnedSources = baseline.pinnedGoSources.map((entry) => verify(entry));
const toolchain = verify(baseline.toolchain);
const tables = json(`${reviewPath}\\HPACK-TABLES-A2.json`);
const tableSources = tables.sourceWitnesses.map((entry) => verify(entry));
const tableSource = readFileSync(join(root, tableSources[0].path), "utf8");
for (const [name, type, data] of [["huffmanCodes", "uint32", tables.huffmanCodes], ["huffmanCodeLen", "uint8", tables.huffmanLengths]]) {
  const declaration = tableSource.match(new RegExp(`var ${name} = \\[256\\]${type}\\{([\\s\\S]*?)\\n\\}`));
  assert.ok(declaration, `Pinned table declaration missing: ${name}`);
  const numbers = declaration[1].replace(/\/\/[^\n]*/g, "").match(/0x[0-9a-f]+|\d+/gi).map(Number);
  assert.deepEqual(numbers, data, `Pinned protocol table differs: ${name}`);
}
assert.equal(tables.staticTable.length, 61);
assert.equal(tables.huffmanCodes.length, 256);
assert.equal(tables.huffmanLengths.length, 256);
const pinnedLicense = baseline.pinnedGoSources.find((entry) => entry.path.endsWith("\\LICENSE"));
assert.equal(tables.license, readFileSync(join(root, pinnedLicense.path), "utf8"));

const names = [
  ["compile", "successor-a2-compile-20260921-01", 0],
  ["calibration", "successor-a2-h2-calibration-20260921-01", 0],
  ["originalEight", "successor-a2-original-eight-20260921-01", 0],
  ["http2", "successor-a2-h2-red-20260921-01", 1],
  ["lifetime", "successor-a2-lifetime-red-20260921-01", 1],
];
const canonicalInputs = (files) => JSON.stringify(files.map(({ path, bytes, sha256 }) => ({ path, bytes, sha256 }))
  .sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
let inputTree;
const runs = {};
for (const [kind, name, expectedExit] of names) {
  const prefix = `.artifacts\\ai-assessments-v1\\${name}`;
  const receipt = json(`${prefix}.receipt.json`);
  const input = json(`${prefix}.inputs.json`);
  const events = readFileSync(join(root, `${prefix}.jsonl`), "utf8").split(/\r?\n/).filter((line) => line.startsWith("{")).map(JSON.parse);
  const tests = events.filter((event) => event.Test && ["pass", "fail", "skip"].includes(event.Action))
    .map(({ Test, Action, Elapsed }) => ({ test: Test, action: Action, elapsedSeconds: Elapsed }));
  const final = events.findLast((event) => !event.Test && ["pass", "fail"].includes(event.Action));
  assert.equal(receipt.exitCode, expectedExit, name);
  assert.equal(receipt.hardTimeout, false, name);
  assert.equal(receipt.outputBoundExceeded, false, name);
  assert.equal(receipt.workingCopiesRemoved, true, name);
  assert.equal(receipt.inputs.allStable, true, name);
  assert.equal(receipt.inputs.count, 890, name);
  assert.equal(input.allInputsStable, true, name);
  assert.deepEqual(input.inputDrift, [], name);
  assert.equal(receipt.inputs.sha256, witness(`${prefix}.inputs.json`).sha256, name);
  input.files.forEach((entry) => verify(entry));
  assert.equal(receipt.goExecutable.sha256, toolchain.sha256, name);
  assert.equal(final?.Action, expectedExit === 0 ? "pass" : "fail", name);
  assert.ok(tests.every((test) => test.action !== "skip"), name);
  const tree = sha(canonicalInputs(input.files));
  if (inputTree === undefined) inputTree = tree;
  assert.equal(tree, inputTree, `Run input tree differs: ${name}`);
  runs[kind] = {
    name, exitCode: receipt.exitCode, startedAt: receipt.startedAt, finishedAt: receipt.finishedAt,
    packageResult: final.Action, packageElapsedSeconds: final.Elapsed, tests, inputCount: 890, inputTreeSHA256: tree,
    files: ["jsonl", "log", "receipt.json", "inputs.json"].map((extension) => witness(`${prefix}.${extension}`)),
  };
}
const outputs = (kind) => readFileSync(join(root, runs[kind].files.find((file) => file.path.endsWith(".log")).path), "utf8");
const occurrences = (text, value) => text.split(value).length - 1;
assert.equal(runs.compile.tests.length, 0);
assert.equal(runs.calibration.tests.length, 3);
assert.ok(runs.calibration.tests.every((test) => test.action === "pass"));
assert.match(outputs("calibration"), /sameConnection=true distinctStreams=true identicalBody=true streams=1,3/);
assert.match(outputs("calibration"), /verifiedTLS=1 rewindable=true actualPOSTHeaders=2 completePOSTBodies=2 refused=1/);
assert.match(outputs("calibration"), /verifiedTLS=1 rewindable=false actualPOSTHeaders=1 completePOSTBodies=1 refused=1/);
assert.equal(runs.originalEight.tests.filter((test) => !test.test.includes("/")).length, 8);
assert.equal(runs.originalEight.tests.filter((test) => test.test.includes("/")).length, 2);
assert.ok(runs.originalEight.tests.every((test) => test.action === "pass"));
assert.equal(runs.http2.tests.length, 1);
assert.match(outputs("http2"), /protocol=h2 verifiedTLS=1 actualPOSTHeaders=2 completePOSTBodies=2 refused=1 DBattempts=1 durableWindowCharges=1 state=succeeded/);
assert.match(outputs("http2"), /ONE recorded attempt\/quota charge emitted more than ONE actual native POST stream/);
assert.match(outputs("http2"), /refused single native attempt committed a successful internal retry output/);
assert.match(outputs("http2"), /A2 HTTP2 executed observation tail/);
assert.equal(runs.lifetime.tests.length, 3);
assert.ok(runs.lifetime.tests.every((test) => test.action === "fail"));
assert.equal(occurrences(outputs("lifetime"), "secondPOSTBeforeFirstTermination=true firstSocketAndContextLive=true beforeConservativeRequestDeadline=true"), 2);
assert.equal(occurrences(outputs("lifetime"), "maximumLiveLocalSockets=2 closedSockets=0"), 2);
assert.equal(occurrences(outputs("lifetime"), "preAdmissionWindowCharges=1"), 2);
assert.equal(occurrences(outputs("lifetime"), "A2 executed cleanup tail:"), 2);
assert.equal(occurrences(outputs("lifetime"), "post-termination admission branch NOT RUN:"), 2);
const publishedBuild = ["json", "log"].map((extension) => witness(`.artifacts\\ai-assessments-v1\\${runs.originalEight.name}.published-v7-build.${extension}`));
const upgradeMatch = outputs("originalEight").match(/actual published-v7 upgrade observation: (.+\.json)/);
assert.ok(upgradeMatch);
const upgrade = witness(relative(root, upgradeMatch[1].trim()));
assert.deepEqual(json(upgrade.path).afterCurrentOpen, ["1", "2", "3", "4", "5", "6", "7", "8"]);
assert.equal(existsSync(join(root, ".cache", "ai-assessments-a2", runs.calibration.name)), false);
assert.equal(existsSync(join(root, ".cache", "ai-assessments-v1", "published-v7", runs.originalEight.name)), false);
const privateRuntime = process.argv[2];
assert.ok(privateRuntime, "Explicit selected private runtime file required for in-memory canary scan");
const selected = JSON.parse(readFileSync(privateRuntime, "utf8"));
const database = new URL(selected.databaseUrl);
const storage = new URL(selected.s3Endpoint);
assert.equal(database.hostname, "127.0.0.1");
assert.equal(database.port, "15432");
assert.equal(storage.hostname, "127.0.0.1");
assert.equal(storage.port, "18333");
const sensitive = [selected.databaseUrl, selected.s3AccessKey, selected.s3SecretKey, selected.postgresPassword, decodeURIComponent(database.password)].filter(Boolean);
const privateCanaries = [...new Set(sensitive.flatMap((value) => [value, encodeURIComponent(value), JSON.stringify(value).slice(1, -1)]))];
const publicEvidence = [...Object.values(runs).flatMap((run) => run.files), ...publishedBuild, upgrade];
for (const entry of publicEvidence) {
  const content = readFileSync(join(root, entry.path), "utf8");
  assert.ok(privateCanaries.every((value) => !content.includes(value)), "Selected private runtime canary found; values withheld");
}

const generated = [];
const emit = (name, value) => {
  const path = `${reviewPath}\\${name}`;
  assert.equal(existsSync(join(root, path)), false, `Refusing to replace frozen evidence: ${name}`);
  writeFileSync(join(root, path), JSON.stringify(value, null, 2) + "\n", { flag: "wx" });
  generated.push(witness(path));
};
const capturedAt = new Date().toISOString();
emit("PROVENANCE-A2.json", {
  schemaVersion: 1, capturedAt, author, successorBaseline: witness(baselinePath),
  inheritedDrafts: baseline.inheritedDrafts, archivedDrafts: archives, inheritedUnsealedRuns: inheritedRuns,
  predecessorEvidenceIsNotSuccessorProof: true, predecessorPacketA2DidNotExistAtSuccessorStart: true,
  preservation: "Original BEFORE-A2, tables and thin calibration wrapper remain exact; successor does not claim original draft authorship.",
  clarification: {
    recordedBeforeSuccessorExecution: true, contract: witness(`${reviewPath}\\CONTRACT-A2.txt`),
    h1AlternativeWithdrawn: true, requiredCapability: "Offered and negotiated certificate/hostname-verified h2 with actual POST HEADERS/body observations.",
    h1OnlyRestrictionRequiresParentDecision: true, productionAlgorithmPrescribed: false,
  },
});
emit("SOURCE-WITNESSES-A2.json", {
  schemaVersion: 1, capturedAt, heldHandoff: verify(baseline.heldHandoff), heldSource19: held,
  pinnedGoVersion: "go1.27.1 windows/amd64", toolchain, pinnedGoSources: pinnedSources,
  protocolTables: witness(`${reviewPath}\\HPACK-TABLES-A2.json`), tableSources,
  tableVerification: { codesEqualPinnedSource: true, lengthsEqualPinnedSource: true, dimensions: [256, 256, 61], licenseExactlyMatchesPinnedGoLicense: true },
  sourceHypothesesConfirmedBySeparateNativeRuns: [
    { issue: "A2R1", traces: ["internal\\app\\assessment_client.go:54-63", "internal\\providers\\assessor.go:93", "net\\http\\http2.go:329", "net\\http\\internal\\http2\\transport.go:426-428,507-542"], run: runs.http2.name },
    { issue: "A2R2", traces: ["internal\\app\\assessment_jobs.go:189-192", "internal\\app\\assessment_claim.go:70-84,126-134,197-281"], run: runs.lifetime.name },
  ],
  sourceTraceAloneIsNotAcceptance: true, installedTransportUnmodified: true, addedDependencies: [],
});
emit("CALIBRATION-A2.json", {
  schemaVersion: 1, capturedAt, compile: runs.compile, transport: runs.calibration, originalEight: runs.originalEight,
  publishedV7Build: publishedBuild, populatedV7UpgradeObservation: upgrade,
  observations: {
    replayable: { normallyVerifiedTLSConnections: 1, negotiatedProtocol: "h2", streams: [1, 3], actualPOSTHeaders: 2, completeIdenticalBodies: 2, refusedStreams: 1, transportChanged: false, requestGetBodyPresent: true },
    discrimination: { normallyVerifiedTLSConnections: 1, negotiatedProtocol: "h2", actualPOSTHeaders: 1, completeBodies: 1, refusedStreams: 1, actualTransportError: true, transportChanged: false, requestGetBodyPresent: false, fixtureOnly: true },
    originalEight: { topLevelPass: 8, nestedPass: 2, coverageOfNewBugsClaimed: false },
  },
  separateParentResult: { elapsedSeconds: 26.021, inputCount: 303, inputTreeSHA256: "64bc3709d47f5f7e98c27ca7181363f42bbe303c6aeab7e4c37e5a03b0071eab", suppliedByParent: true, notReusedAsSuccessorEvidence: true },
  limitations: ["No real provider/account, model quality or compute/billing exactly-once evidence.", "Nonrewindable calibration request is not a production fix or prescribed algorithm.", "Original eight passing does not accept AA9 or AA10."],
});
emit("RED-A2.json", {
  schemaVersion: 1, capturedAt, status: "ACTUAL NATIVE RED - HELD CANDIDATE NOT ACCEPTED", compilerBlocked: false,
  http2: {
    run: runs.http2, actual: { verifiedTLS: true, negotiatedProtocol: "h2", actualPOSTHeaders: 2, completePOSTBodies: 2, refusedStreams: 1, databaseAttempts: 1, durableWindowCharges: 1, publicState: "succeeded" },
    failedAssertions: ["One durable attempt/window charge must emit exactly one actual h2 POST.", "A refused one-attempt job must not commit successful internal-retry output."],
    executedTails: ["Later ProcessNext(false,nil) with no additional HEADERS/body.", "No finding/domain mutation or assessment S3 access."],
    blockedTails: [], notClaimed: ["Exactly-once remote compute or billing", "Safety inferred from an outer RoundTrip counter", "H1-only capability accepted"],
  },
  localLifetime: {
    run: runs.lifetime, maxConcurrent: 1, requestsPerWindow: 8, requestTimeoutMillis: 3000, authorizationMillis: 25, leaseMillis: 250,
    fixturePoolSlots: 2, eachIndependentWorkerPoolSlots: 1, queryGate: "Real first worker SELECT app_workspaces ... FOR SHARE before query execution; scheduling/read-only observation only.",
    scenarios: [
      { name: "explicit-cancel", committedPublicState: "cancelled" },
      { name: "expired-lease", committedPublicState: "uncertain", actualDatabaseClockExpiry: true, injectedClockOrLeaseWrite: false },
    ].map((scenario) => ({
      ...scenario, preAdmissionWindowCharges: 1, actualNativePOSTs: 2, simultaneousLocalSockets: 2, closedSocketsAtOverlap: 0,
      firstSocketAndRequestContextLive: true, secondSocketAndRequestContextLive: true, firstQueryStillGated: true,
      beforeConservativeRequestDeadline: true,
      failedAssertion: "Second complete native POST must not start while the retired first operation still occupies local I/O capacity.",
      executedTails: ["Gate released.", "First real net.Conn.Close delegated and observed.", "Cooperative native server sees request cancellation.", "First worker returns.", "Already-admitted second held request completes once after first Close.", "Old receipt unchanged, result absent, attempts=1.", "Later scheduling does not resend either terminal job.", "Both sockets close; domain/storage unchanged."],
      blockedTails: ["Success-path post-termination admission of a still-queued second job was not run because RED already admitted it before termination."],
    })),
    notClaimed: ["Remote compute termination or concurrency", "Cancellation-ignoring native fixture", "A new schema column, API or reservation algorithm prescribed"],
  },
  backendCoderRemainsHold: true, parentMustVerifyAndSealBeforeSeparateFix: true,
});
emit("GUARDS-A2.json", {
  schemaVersion: 1, capturedAt, baseline: witness(baselinePath), unchangedHeldBackendCount: held.length,
  heldHandoff: verify(baseline.heldHandoff), currentA1Manifest: verify(baseline.oldA1Controls.manifest),
  currentA1Signature: verify(baseline.oldA1Controls.signature), unchangedCurrentA1Controls: controls.length,
  publicOracleConfigSHA256: baseline.oldA1Controls.publicOracleConfigSHA256,
  unchangedExistingBackendControlRuntimeCount: existing.length, allBeforeAfterHashesEqual: true, mismatches: [],
  exactGuardSet: "BEFORE-A2-SUCCESSOR.json backendAndExistingControls; checked before/after each run and again at packet generation.",
  excludedMutablePaths: ["web\\**", "README.md"], exclusionsAreNotUIAcceptance: true,
  separateRuntimePacket: runtimeFiles.find((entry) => entry.path.endsWith("\\PACKET.json")), unchangedSeparateRuntimeFiles: runtimeFiles.length,
  originalEightFixturesBindingsRunnersPoolsBudgetsAndCompatibilityBytesUnchanged: true,
  goModAndGoSumUnchanged: ["go.mod", "go.sum"].map(witness),
  toolchainAndPinnedSourcesUnchanged: true, inputTreeSHA256: inputTree, runInputCount: 890,
  canonicalInputTreeEncoding: "SHA256(UTF8(JSON.stringify([{path,bytes,sha256},...] sorted by ordinal path))).",
  allFiveRunsUseSameSourceInputTree: true,
  authority: { privateAuthorityAccess: false, oracleCapture: false, resigning: false, commits: false, agents: false, productionChanges: false, runtimeOrOSOrClusterOrPolicyChanges: false },
  cleanup: { perRunCalibrationCopiesRemoved: true, selectedPerRunPublishedV7CopiesRemoved: true, unrelatedCachesOrUsersOrSchemasOrVolumesTouched: false },
  privacy: { inMemorySelectedRuntimeCanaryScan: true, publicRunArtifactsScanned: publicEvidence.length, privateValuesPersisted: false, matches: 0 },
});
emit("EVIDENCE-A2.json", {
  schemaVersion: 1, capturedAt, runs, publishedV7Build: publishedBuild, populatedUpgrade: upgrade,
  independentSuccessorReproduction: true, inputTreeSHA256: inputTree, sourceInputCount: 890,
  ownership: "Fresh random schemas and selected legitimate finding-intake prefixes on existing owned PG15432/S318333; owned ephemeral verified-TLS native endpoints; no real providers/accounts.",
  tests: ["tests\\ai_assessments\\transport_replay_test.go", "tests\\ai_assessments\\concurrency_lifetime_test.go"].map(witness),
  sourceStableRed: true, behavioralRedNotCompilerFailure: true, predecessorUnsealedLogsNotUsedForAcceptance: true,
});
const frozenFiles = [
  ...["CONTRACT-A2.txt", "BEFORE-A2.json", "BEFORE-A2-SUCCESSOR.json", "HPACK-TABLES-A2.json", "Test-Native-A2.ps1", "calibrate-http2-A2.go.txt", "HANDOFF-A2.txt", "freeze-A2.mjs"].map((name) => witness(`${reviewPath}\\${name}`)),
  witness("tests\\ai_assessments\\transport_replay_test.go"), witness("tests\\ai_assessments\\concurrency_lifetime_test.go"),
  ...archives.map((entry) => entry.finalArchive), ...generated, ...publicEvidence,
];
const unique = [...new Map(frozenFiles.map((entry) => [entry.path, entry])).values()];
for (const entry of unique) verify(entry);
emit("PACKET-A2.json", {
  schemaVersion: 1, capturedAt, stage: "Independent backend A2 successor regression authorship", author,
  status: "FROZEN ACTUAL NATIVE RED - PARENT VERIFY/SEAL, THEN SEPARATE CODER",
  signed: false, independentAcceptance: false, M11Complete: false, productionFixImplemented: false,
  predecessorProvenance: `${reviewPath}\\PROVENANCE-A2.json`,
  contract: `${reviewPath}\\CONTRACT-A2.txt`, handoff: `${reviewPath}\\HANDOFF-A2.txt`,
  heldHandoffSHA256: baseline.heldHandoff.sha256, heldBackendPaths: 19,
  currentA1ManifestSHA256: baseline.oldA1Controls.manifest.sha256, unchangedA1Controls: 221,
  separateRuntimePacketSHA256: "1718bd25a829fd3b886f9d5dcef1ba62377bdfa4c86b0d565eb79cc3b154cc34",
  requiredH2CapabilityNotH1Fallback: true, inputTreeSHA256: inputTree, runInputCount: 890,
  outcome: { compile: "PASS", fixtureCalibration: "PASS (2 nested controls)", unchangedOriginalEight: "PASS (8 top-level + 2 nested)", newHTTP2: "RED (2 actual POSTs/1 durable attempt)", newLocalLifetime: "RED (cancel and actual lease expiry both overlap local I/O)" },
  unexecutedBranch: "AA10 correct post-termination admission branch is not claimed: the held candidate already admitted the second job before termination.",
  guardExclusions: ["web\\**", "README.md"],
  boundaries: ["No production, old control, runtime or UI changes.", "No provider accounts, installations, agents, commits, authority capture or resigning.", "No remote-compute/billing exactly-once or remote concurrency certification."],
  fileCount: unique.length, files: unique,
});
const packet = witness(`${reviewPath}\\PACKET-A2.json`);
writeFileSync(join(review, "PACKET-A2.sha256"), `${packet.sha256}  PACKET-A2.json\n`, { flag: "wx" });
existing.forEach((entry) => verify(entry));
const manifest = json(`${reviewPath}\\PACKET-A2.json`);
manifest.files.forEach((entry) => verify(entry));
assert.equal(manifest.fileCount, manifest.files.length);
assert.equal(statSync(join(review, "PACKET-A2.json")).size, packet.bytes);
assert.equal(readdirSync(join(review, "history", "a2-interrupted")).some((name) => name.endsWith(".go")), false);
console.log(JSON.stringify({ packet, frozenFileCount: manifest.fileCount, inputTreeSHA256: inputTree, unchangedGuardCount: existing.length, newNativeOutcome: "RED", oldEightAndCalibration: "PASS", stopForParent: true }, null, 2));
