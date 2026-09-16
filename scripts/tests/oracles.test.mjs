import assert from "node:assert/strict";
import { createHash, generateKeyPairSync, sign, verify } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const repository = fileURLToPath(new URL("../../", import.meta.url));
const implementationURL = new URL("../oracle-lib.mjs", import.meta.url);
const implementation = existsSync(implementationURL) ? await import(implementationURL) : null;
const digest = (data) => createHash("sha256").update(data).digest("hex");

function fixture(t) {
  const parent = join(repository, ".cache", "oracle-tests");
  mkdirSync(parent, { recursive: true });
  const root = mkdtempSync(join(parent, "case-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const put = (path, data) => {
    mkdirSync(dirname(join(root, path)), { recursive: true });
    writeFileSync(join(root, path), data);
  };
  put("tests/security_test.go", "package protected\nfunc TestBoundary() { requireDenied() }\n");
  put("tests/helper.go", "package protected\nconst RequiredRole = \"analyst\"\n");
  put("compiler.json", '{"strict":true,"skipTests":false}\n');
  put("production.go", "package service\nconst Enabled = true\n");
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  put("authority.pem", publicKey.export({ type: "spki", format: "pem" }));
  const manifest = {
    schemaVersion: 1, scope: "security-fixture", phase: "pre-code-red",
    author: "independent-test-author", capturedAt: "2026-09-15T00:00:00Z",
    redEvidence: { sha256: digest("synthetic RED log"), exitCode: 1 },
    protectedRoots: ["tests"], bindingPaths: ["tests/production_bindings_test.go"],
    files: ["tests/security_test.go", "tests/helper.go", "compiler.json"].map((path) => ({
      path, normalization: "none", sha256: digest(readFileSync(join(root, path))),
    })),
  };
  const bytes = Buffer.from(JSON.stringify(manifest, null, 2) + "\n");
  put("oracle.json", bytes);
  put("oracle.json.sig", sign(null, bytes, privateKey).toString("base64"));
  return {
    root, put, manifest, bytes, privateKey, publicKey,
    options: { root, manifestPath: join(root, "oracle.json"), signaturePath: join(root, "oracle.json.sig"), trustedKeyPath: join(root, "authority.pem") },
  };
}

function api() {
  assert.ok(implementation, "Protected-oracle implementation missing; tests must precede its code.");
  assert.equal(typeof implementation.verifyOracle, "function");
  assert.equal(typeof implementation.treeDigest, "function");
  assert.equal(typeof implementation.checkBinding, "function");
  return implementation;
}

test("oracle harness uses actual Ed25519 signatures", (t) => {
  const f = fixture(t);
  const signature = sign(null, f.bytes, f.privateKey);
  assert.equal(verify(null, f.bytes, f.publicKey, signature), true);
  assert.equal(verify(null, Buffer.concat([f.bytes, Buffer.from("changed")]), f.publicKey, signature), false);
});

test("unchanged signed test-author files verify without rewriting them", (t) => {
  const guard = api();
  const f = fixture(t);
  const before = readFileSync(join(f.root, "tests/security_test.go"));
  const result = guard.verifyOracle(f.options);
  assert.equal(result.scope, f.manifest.scope);
  assert.equal(result.phase, "pre-code-red");
  assert.equal(result.fileCount, f.manifest.files.length);
  assert.deepEqual(readFileSync(join(f.root, "tests/security_test.go")), before);
});

test("missing or untrusted authority never falls back to a bundled or generated key", (t) => {
  const guard = api();
  const f = fixture(t);
  assert.throws(() => guard.verifyOracle({ ...f.options, trustedKeyPath: undefined }), /trusted|authority|key/i);
  const other = generateKeyPairSync("ed25519");
  f.put("wrong-authority.pem", other.publicKey.export({ type: "spki", format: "pem" }));
  assert.throws(() => guard.verifyOracle({ ...f.options, trustedKeyPath: join(f.root, "wrong-authority.pem") }), /signature|authority/i);
});

test("changed tests, helpers, compiler controls, and added bypass tests are rejected", (t) => {
  const guard = api();
  const f = fixture(t);
  for (const path of ["tests/security_test.go", "tests/helper.go", "compiler.json"]) {
    const original = readFileSync(join(f.root, path));
    f.put(path, Buffer.concat([original, Buffer.from("\nchanged\n")]));
    assert.throws(() => guard.verifyOracle(f.options), /changed|hash|digest|oracle/i);
    f.put(path, original);
  }
  f.put("tests/bypass_test.go", "package protected\nfunc TestMain() {}\n");
  assert.throws(() => guard.verifyOracle(f.options), /unlisted|unprotected|unexpected|oracle/i);
});

test("rewriting manifest hashes without test-author approval is rejected", (t) => {
  const guard = api();
  const f = fixture(t);
  f.put("tests/security_test.go", "package protected\nfunc TestBoundary() {}\n");
  f.manifest.files[0].sha256 = digest(readFileSync(join(f.root, "tests/security_test.go")));
  f.put("oracle.json", JSON.stringify(f.manifest));
  assert.throws(() => guard.verifyOracle(f.options), /signature|authority/i);
});

test("bindings cannot suppress tests or implement substitute I/O", () => {
  const guard = api();
  const valid = "package boundary\nimport \"context\"\nfunc open(ctx context.Context, c Config) (Adapter,error) { return implementation.Open(ctx, c) }\n";
  assert.doesNotThrow(() => guard.checkBinding(valid, "binding_test.go"));
  for (const source of [
    'package boundary\nimport "os"\nfunc TestMain(m *testing.M) { os.Exit(0) }\n',
    'package boundary\nfunc fake() { db.Query(context.Background(), "SELECT 1") }\n',
    'package boundary\nimport "net/http/httptest"\nfunc fake() { httptest.NewRecorder() }\n',
    'package boundary\nfunc fake() { time.Sleep(time.Second) }\n',
    'package boundary\nfunc fake() { http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("success")) }) }\n',
  ]) assert.throws(() => guard.checkBinding(source, "binding_test.go"), /binding|forbidden|test|substitute/i);
});

test("review digest changes with exact production or oracle inputs, not generated output", (t) => {
  const guard = api();
  const f = fixture(t);
  const options = { root: f.root, files: ["production.go", "tests/security_test.go", "compiler.json", "oracle.json", "oracle.json.sig"] };
  const first = guard.treeDigest(options);
  assert.match(first, /^sha256:[a-f0-9]{64}$/);
  assert.equal(guard.treeDigest(options), first);
  f.put("generated.log", "not a source input");
  assert.equal(guard.treeDigest(options), first);
  f.put("production.go", "package service\nconst Enabled = false\n");
  assert.notEqual(guard.treeDigest(options), first);
});

test("oracle paths cannot escape the source root", (t) => {
  const guard = api();
  const f = fixture(t);
  f.manifest.files[0].path = "../outside_test.go";
  const bytes = Buffer.from(JSON.stringify(f.manifest));
  f.put("oracle.json", bytes);
  f.put("oracle.json.sig", sign(null, bytes, f.privateKey).toString("base64"));
  assert.throws(() => guard.verifyOracle(f.options), /path|root|outside|escape/i);
});
