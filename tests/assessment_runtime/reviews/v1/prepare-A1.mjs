import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readdirSync, rmdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const review = dirname(fileURLToPath(import.meta.url));
const root = resolve(review, "..", "..", "..", "..");
const source = join(root, "tests", "assessment_runtime");
const hash = (value) => createHash("sha256").update(value).digest("hex");
const before = readFileSync(join(review, "history", "a1", "fixtures_test.go.txt"), "utf8");
const originalWait = `func wait(t *testing.T, ctx context.Context, label string, ready func() bool) {
\tt.Helper()
\ttick := time.NewTicker(20 * time.Millisecond)`;
const amendedWait = `func wait(t *testing.T, ctx context.Context, label string, ready func() bool) {
\tt.Helper()
\twaitAt(t, ctx, label, 20*time.Millisecond, ready)
}
func waitAt(t *testing.T, ctx context.Context, label string, cadence time.Duration, ready func() bool) {
\tt.Helper()
\ttick := time.NewTicker(cadence)`;
const originalAwait = `wait(c.f.t, ctx, "real durable assessment terminal state", func() bool {`;
const amendedAwait = `waitAt(c.f.t, ctx, "real durable assessment terminal state", 100*time.Millisecond, func() bool {`;
assert.equal(before.split(originalWait).length, 2);
assert.equal(before.split(originalAwait).length, 2);
const amended = before.replace(originalWait, amendedWait).replace(originalAwait, amendedAwait);
const operation = process.argv[2];
if (operation === "verify-cadence") {
  assert.equal(readFileSync(join(source, "fixtures_test.go"), "utf8"), amended, "Only the approved terminal HTTP observation cadence may change");
  process.exit(0);
}
const label = process.argv[3];
assert.match(label ?? "", /^[a-z0-9]+(?:-[a-z0-9]+)*$/);
assert.ok(label.length <= 79);
const work = join(root, ".cache", "assessment-runtime-a1", label);
const receipt = join(root, ".artifacts", "assessment-runtime-v1", `${label}.diagnostic-inputs.json`);
if (operation === "cleanup") {
  const manifest = JSON.parse(readFileSync(receipt));
  for (const entry of manifest.copies) {
    assert.equal(hash(readFileSync(join(root, entry.copy))), entry.sha256, entry.copy);
    rmSync(join(root, entry.copy));
  }
  assert.equal(readdirSync(work).length, 0);
  rmdirSync(work);
  console.log("Only selected diagnostic copies removed; original fixtures/binding/roles and production unchanged.");
  process.exit(0);
}
assert.equal(operation, "prepare");
assert.equal(existsSync(work), false);
mkdirSync(work, { recursive: true });
const copies = [];
const writeCopy = (name, bytes, origin) => {
  const target = join(work, name);
  writeFileSync(target, bytes, { flag: "wx" });
  copies.push({ source: origin, copy: relative(root, target), bytes: Buffer.byteLength(bytes), sha256: hash(bytes) });
};
for (const name of readdirSync(source).filter((name) => name.endsWith("_test.go"))) {
  writeCopy(name, readFileSync(join(source, name)), relative(root, join(source, name)));
}
const roles = readFileSync(join(source, "roles_test.go"), "utf8");
let ar4 = roles.slice(roles.indexOf("func TestAR4ContextCancellationAndFreshCommandPreserveDispatchUncertaintyAndQuota"));
const original = ar4;
const substitutions = [
  ["TestAR4ContextCancellationAndFreshCommandPreserveDispatchUncertaintyAndQuota", "TestA1AR4ReadonlyDiagnostic"],
  ["f := newFixture(t)", "f := newFixture(t)\n\t\t\tobserve := newA1Observation(t, f, kind)\n\t\t\tdefer observe.bodyDone()"],
  ["n := newNative(f)", "n := newNative(f)\n\t\t\tobserve.native(n)"],
  ["core := f.coreService(f.scope, f.key, false)", "core := f.coreService(f.scope, f.key, false)\n\t\t\tobserve.api(core)"],
  ["held := n.arm(job, key, true)", "held := n.arm(job, key, true)\n\t\t\tobserve.held = held"],
  ["config.RequestsPerWindow = 1", "config.RequestsPerWindow = 1\n\t\t\tobserve.transport(config.Client)"],
  ["command = startCommand(t, f, config)", "command = startCommand(t, f, config)\n\t\t\t\tobserve.pid(\"original-command-started\", command)"],
  ["ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)", "ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)\n\t\t\tobserve.setShared(ctx)\n\t\t\tobserve.mark(\"shared-context-created\")"],
  ["var originalMarker time.Time", "observe.mark(\"native-ready-observed\")\n\t\t\tvar originalMarker time.Time"],
  ["check(t, attempts == 1, \"native dispatch marker was not persisted once\")", "check(t, attempts == 1, \"native dispatch marker was not persisted once\")\n\t\t\tobserve.mark(\"marker-read-complete\")"],
  ["if run != nil {\n\t\t\t\tstopRole(t, run)", "observe.mark(\"quota-read-complete\")\n\t\t\tif run != nil {\n\t\t\t\tobserve.mark(\"before-stopRole\")\n\t\t\t\tstopRole(t, run)\n\t\t\t\tobserve.mark(\"after-stopRole-and-listener-check\")"],
  ["command.kill(t)\n\t\t\t}", "observe.pid(\"crashed-command\", command)\n\t\t\t\tcommand.kill(t)\n\t\t\t\tobserve.pid(\"crashed-command-cleaned\", command)\n\t\t\t}"],
  ["event(t, ctx, held.cancelled, \"actual native HTTP context cancellation\")", "observe.mark(\"before-native-cancel-event\")\n\t\t\tevent(t, ctx, held.cancelled, \"actual native HTTP context cancellation\")\n\t\t\tobserve.mark(\"native-cancel-event-observed\")"],
  ["fresh := startCommand(t, f, config)", "fresh := startCommand(t, f, config)\n\t\t\tobserve.pid(\"fresh-command\", fresh)"],
  ["fresh.kill(t)\n\t\t})", "fresh.kill(t)\n\t\t\tobserve.pid(\"fresh-command-cleaned\", fresh)\n\t\t\tobserve.snapshot(\"completed-tail\")\n\t\t\tobserve.mark(\"all-original-AR4-subcase-assertions-reached\")\n\t\t})"],
];
for (const [from, to] of substitutions) {
  assert.equal(ar4.split(from).length, 2, from);
  ar4 = ar4.replace(from, to);
}
let reconstructed = ar4;
for (const [from, to] of [...substitutions].reverse()) reconstructed = reconstructed.replace(to, from);
assert.equal(reconstructed, original, "Diagnostic extraction changed an original AR4 assertion or statement");
const header = `//go:build integration\n\npackage assessment_runtime\n\nimport ("context";"reflect";"testing";"time")\n\n`;
writeCopy("a1_diagnostic_test.go", header + ar4, "Exact original AR4 body plus reversible readonly observations; roles_test.go unmodified");
writeCopy("a1_observer_test.go", readFileSync(join(review, "observe-A1.go.txt")), relative(root, join(review, "observe-A1.go.txt")));
writeFileSync(receipt, JSON.stringify({
  schemaVersion: 1, label, copies, originalAR4SHA256: hash(original), reversibleAssertionPreservationVerified: true,
  actualCommandNotSubstituted: true, typedProductionBindingCopiedByteExact: true,
  readonlyObservationOnly: true, workerPoolsLimitsBudgetsDeadlinesAndAssertionsUnchanged: true,
}, null, 2) + "\n", { flag: "wx" });
console.log("Prepared byte-exact helpers/binding plus reversible readonly AR4 diagnostic; actual production command required.");
