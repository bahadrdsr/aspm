import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readdirSync, rmdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const review = dirname(fileURLToPath(import.meta.url));
const root = resolve(review, "..", "..", "..", "..");
const source = join(root, "tests", "assessment_runtime");
const hash = (value) => createHash("sha256").update(value).digest("hex");
const archived = (name) => readFileSync(join(review, "history", "a2", `${name}.txt`), "utf8");
const current = (name) => readFileSync(join(source, name), "utf8");
const oldCall = `event(t, ctx, held.cancelled, "actual native HTTP context cancellation")`;
const newCall = `nativeCancellation(t, ctx, held, "actual native HTTP context cancellation")`;
const roles = current("roles_test.go");
assert.equal(roles.split(newCall).length, 2);
assert.equal(roles.replace(newCall, oldCall), archived("roles_test.go"), "Only AR4 post-stop observer call may change");
const native = current("native_test.go");
assert.equal(native.match(/^\tcancelledAt\s+time.Time$/gm)?.length, 1);
assert.equal(native.split("\t\t\tr.cancelledAt = time.Now()\n\t\t\tclose(r.cancelled)").length, 2);
assert.equal(native.replace(/^\tcancelledAt\s+time.Time\n/m, "").replace("\t\t\tr.cancelledAt = time.Now()\n", ""), archived("native_test.go"),
  "Only actual native cancellation timestamp field and pre-close assignment may change");
const fixture = current("fixtures_test.go");
const begin = fixture.indexOf("func cancellationBeforeDeadline(");
const end = fixture.indexOf("type coreAPI struct", begin);
assert.ok(begin > 0 && end > begin);
const addition = fixture.slice(begin, end);
assert.equal(fixture.slice(0, begin) + fixture.slice(end), archived("fixtures_test.go"), "Other helpers, A1 cadence, budgets, timeouts and checks must be exact");
for (const forbidden of ["context.WithTimeout", "context.WithDeadline", "time.Now()", "time.Sleep", "time.NewTimer"]) assert.ok(!addition.includes(forbidden), forbidden);
assert.match(addition, /ctx\.Deadline\(\)/);
assert.match(addition, /request\.cancelledAt\.IsZero\(\)/);
assert.match(addition, /request\.cancelledAt\.Before\(deadline\)/);
assert.match(addition, /request\.cancelledAt\.Equal\(deadline\)/);
const operation = process.argv[2];
if (operation === "verify-amendment") process.exit(0);
const label = process.argv[3];
assert.match(label ?? "", /^[a-z0-9]+(?:-[a-z0-9]+)*$/);
assert.ok(label.length <= 79);
const work = join(root, ".cache", "assessment-runtime-a2", label);
const receipt = join(root, ".artifacts", "assessment-runtime-v1", `${label}.calibration-inputs.json`);
if (operation === "cleanup") {
  const manifest = JSON.parse(readFileSync(receipt));
  for (const entry of manifest.copies) {
    assert.equal(hash(readFileSync(join(root, entry.copy))), entry.sha256, entry.copy);
    rmSync(join(root, entry.copy));
  }
  assert.equal(readdirSync(work).length, 0);
  rmdirSync(work);
  console.log("Only selected calibration test copies removed; actual command and production not substituted.");
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
for (const name of readdirSync(source).filter((name) => name.endsWith("_test.go"))) writeCopy(name, readFileSync(join(source, name)), relative(root, join(source, name)));
let ar4 = roles.slice(roles.indexOf("func TestAR4ContextCancellationAndFreshCommandPreserveDispatchUncertaintyAndQuota"));
const original = ar4;
const substitutions = [
  ["TestAR4ContextCancellationAndFreshCommandPreserveDispatchUncertaintyAndQuota", "TestA2DelayedCancellationObserverKeepsOriginalDeadline"],
  ["f := newFixture(t)", "f := newFixture(t)\n\t\t\tproof := newA2CancellationCalibration(t, f, kind)"],
  ["n := newNative(f)", "n := newNative(f)\n\t\t\tproof.native = n"],
  ["core := f.coreService(f.scope, f.key, false)", "core := f.coreService(f.scope, f.key, false)\n\t\t\tproof.core = core"],
  ["held := n.arm(job, key, true)", "held := n.arm(job, key, true)\n\t\t\tproof.held = held"],
  ["ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)", "ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)\n\t\t\tproof.setOriginalDeadline(ctx)"],
  ["stopRole(t, run)", "proof.stopStarted = time.Now()\n\t\t\t\tstopRole(t, run)\n\t\t\t\tproof.stopReturned = time.Now()\n\t\t\t\tproof.role = run"],
  [newCall, "proof.delayOnlyObserver(ctx)\n\t\t\t" + newCall + "\n\t\t\tproof.realBoundValidated = true\n\t\t\tproof.syntheticCases(ctx)"],
  ["fresh := startCommand(t, f, config)", "fresh := startCommand(t, f, config)\n\t\t\tproof.fresh = fresh"],
  ["fresh.kill(t)\n\t\t})", "fresh.kill(t)\n\t\t\tproof.originalTailReached = true\n\t\t})"],
];
for (const [from, to] of substitutions) {
  assert.equal(ar4.split(from).length, 2, from);
  ar4 = ar4.replace(from, to);
}
let reconstructed = ar4;
for (const [from, to] of [...substitutions].reverse()) reconstructed = reconstructed.replace(to, from);
assert.equal(reconstructed, original, "Calibration changed an original AR4 assertion/statement");
writeCopy("a2_calibration_test.go", `//go:build integration\n\npackage assessment_runtime\n\nimport ("context";"reflect";"testing";"time")\n\n` + ar4,
  "Amended AR4 body plus reversible observer-only proof statements; selected Run-context only");
writeCopy("a2_measurement_test.go", readFileSync(join(review, "calibrate-A2.go.txt")), relative(root, join(review, "calibrate-A2.go.txt")));
writeFileSync(receipt, JSON.stringify({
  schemaVersion: 1, label, copies, sourceAR4SHA256: hash(original), reversibleAssertionPreservationVerified: true,
  actualCommandNotSubstituted: true, typedBindingCopiedByteExact: true, normalInputsCopiedByteExact: true,
  originalFiveSecondContextAndSixSecondStopRoleUnchanged: true, onlyObserverDelayed: true,
  syntheticNegativesAreNotProductionExecution: true,
}, null, 2) + "\n", { flag: "wx" });
console.log("Prepared exact normal tests/binding and actual AR4 calibration; only observer waits on the original deadline.");
