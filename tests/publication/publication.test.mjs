import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import {
  lstatSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync,
} from "node:fs";
import { dirname, join, posix, resolve } from "node:path";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";

const packet = dirname(fileURLToPath(import.meta.url));
const root = resolve(packet, "..", "..");
const sourcePaths = [
  ".oracles/ui-workflow-review-v2.json", ".oracles/ui-workflow-review-v2.json.sig",
  "go.mod", "go.sum", "scripts/process.mjs", "web/tests/application-fixture.ts",
  "tests/acceptance/Test-Acceptance.ps1", "tests/installer/CONTRACT.txt",
  "tests/installer/contract_test.go",
];
const mustIgnore = [
  "tests/acceptance/.run/build/synthetic-cache.a",
  "tests/acceptance/.run/work/synthetic-work.go",
  "tests/acceptance/.run/modules/example.invalid/synthetic@v0.0.0/go.mod",
  "tests/acceptance/.run/gopath/pkg/synthetic-cache.a",
  "tests/acceptance/.run/current.jsonl",
  "tests/acceptance/.run/private.env",
  "tests/readiness_bootstrap/.run/build/synthetic-cache.a",
  "tests/publication-fixture/deep/.run/work/generated.txt",
  "tests/acceptance/.security-run/build/synthetic-cache.a",
  "tests/acceptance/.security-run/work/synthetic-work.go",
  "tests/acceptance/.security-run/modules/example.invalid/synthetic@v0.0.0/go.mod",
  "tests/acceptance/.security-run/gopath/pkg/synthetic-cache.a",
  "tests/acceptance/.security-run/current.jsonl",
  "tests/analysis/deep/.security-run/current.jsonl",
  ".cache/publication-tests/synthetic/current.jsonl",
  ".cache/node_modules/synthetic/index.js",
  "node_modules/synthetic/index.js",
  "web/node_modules/synthetic/index.js",
  ".artifacts/publication-preview.zip",
  "web/.artifacts/publication-preview.zip",
  "private.env",
];
const mustKeep = [
  ...sourcePaths,
  "cmd/aspmctl/publication_fixture.go", "internal/publication-fixture/policy.go",
  "internal/publication-fixture/policy_test.go", "tests/acceptance/publication_fixture_test.go",
  "web/src/publication-fixture.ts", "web/tests/publication-fixture.spec.ts",
  "tests/acceptance/testdata/publication-source.json", "web/tests/fixtures.ts",
  "LICENSE", "NOTICE", "web/public/notices/radix-MIT.txt",
  "web/tests/upstream/animate-LICENSE.txt", "web/tests/upstream/shadcn-LICENSE.txt",
  "evidence/publication-finding.json", "findings/publication-review.json",
  "vulnerability-findings/publication-review.json", "docs/evidence/publication-review.json",
  "tests/acceptance/evidence/publication-review.json",
  "tests/acceptance/vulnerability-findings/publication-review.json",
  "internal/evidence/publication_fixture.go",
  "tests/publication/.gitattributes", "tests/publication/publication.test.mjs", "tests/publication/CONTRACT.txt",
  "tests/publication/RED.tap", "tests/publication/PACKET.sha256",
];
const trackedHistory = [
  "tests/readiness_bootstrap/.run/red.jsonl", "tests/readiness_bootstrap/.run/red.log",
];
const controlNames = [".gitattributes", "CONTRACT.txt", "inputs.before.json", "publication.test.mjs", "run.mjs"];
const generatedFolders = new Set([".cache", ".artifacts", ".run", ".security-run", "node_modules"]);
const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
const native = (base, name) => {
  assert.ok(name.split("/").every((part) => part && part !== "." && part !== ".." && !/[:\\]/.test(part)));
  return join(base, ...name.split("/"));
};

function regularBytes(base, name, optional = false) {
  const parts = name.split("/");
  let current = base;
  for (const [index, part] of parts.entries()) {
    current = native(current, part);
    let stat;
    try { stat = lstatSync(current); } catch (error) {
      if (optional && error.code === "ENOENT") return undefined;
      throw error;
    }
    assert.ok(!stat.isSymbolicLink(), `No linked input: ${name}`);
    if (index === parts.length - 1) {
      assert.ok(stat.isFile() && stat.nlink === 1, `No nonregular or hard-linked input: ${name}`);
    } else {
      assert.ok(stat.isDirectory(), `Not an input directory: ${name}`);
    }
  }
  return readFileSync(current);
}

function ensureDirectory(base, name) {
  let current = base;
  for (const part of name.split("/")) {
    current = native(current, part);
    try { mkdirSync(current); } catch (error) { if (error.code !== "EEXIST") throw error; }
    const stat = lstatSync(current);
    assert.ok(stat.isDirectory() && !stat.isSymbolicLink(), `Not an owned plain directory: ${name}`);
  }
  return current;
}

function put(base, name, bytes) {
  const parent = posix.dirname(name);
  if (parent !== ".") ensureDirectory(base, parent);
  writeFileSync(native(base, name), bytes, { flag: "wx" });
}

function policySnapshot() {
  const candidates = new Set([".gitattributes", ".gitignore"]);
  for (const name of [...sourcePaths, ...mustIgnore, ...mustKeep, ...trackedHistory]) {
    const parents = name.split("/").slice(0, -1);
    const prefix = [];
    for (const part of parents) {
      if (generatedFolders.has(part)) break;
      prefix.push(part);
      for (const policy of [".gitattributes", ".gitignore"]) candidates.add([...prefix, policy].join("/"));
    }
  }
  const policies = new Map();
  for (const name of [...candidates].sort()) {
    const bytes = regularBytes(root, name, true);
    if (bytes !== undefined) policies.set(name, bytes);
  }
  assert.ok(policies.has(".gitattributes") && policies.has(".gitignore"), "Root policies must exist.");
  return policies;
}

function digestRows(entries) {
  return [...entries].map(([path, bytes]) => ({ path, bytes: bytes.length, sha256: sha256(bytes) }));
}

let owned, ownedParent, owner, sources, policies, controls;
let fixtureNumber = 0;
before(() => {
  const baseline = JSON.parse(regularBytes(packet, "inputs.before.json"));
  assert.equal(baseline.schemaVersion, 1);
  assert.deepEqual(baseline.sources.map((entry) => entry.path), sourcePaths, "Frozen input allowlist changed.");
  sources = new Map(baseline.sources.map((entry) => {
    const bytes = regularBytes(root, entry.path);
    assert.equal(bytes.length, entry.bytes, `Selected source size changed: ${entry.path}`);
    assert.equal(sha256(bytes), entry.sha256, `Selected source bytes changed: ${entry.path}; never recapture automatically.`);
    return [entry.path, bytes];
  }));
  const hashFile = regularBytes(packet, "controls.before.sha256");
  const rows = hashFile.toString("utf8").trimEnd().split(/\r?\n/).map((line) => {
    const match = /^([a-f0-9]{64})  ([A-Za-z0-9.-]+)$/.exec(line);
    assert.ok(match, "Malformed control hash entry.");
    return { sha256: match[1], path: match[2] };
  });
  assert.deepEqual(rows.map((entry) => entry.path), controlNames, "Frozen control allowlist changed.");
  controls = new Map(rows.map((entry) => {
    const bytes = regularBytes(packet, entry.path);
    assert.equal(sha256(bytes), entry.sha256, `Frozen test control changed: ${entry.path}`);
    return [entry.path, bytes];
  }));
  controls.set("controls.before.sha256", hashFile);
  policies = policySnapshot();
  ownedParent = ensureDirectory(root, ".cache/publication-tests");
  owned = mkdtempSync(join(ownedParent, "run-"));
  owner = randomUUID();
  put(owned, "OWNER", owner);
  ensureDirectory(owned, "home");
  ensureDirectory(owned, "empty-template");
  ensureDirectory(owned, "hooks");
  put(owned, "empty-config", "");
});

after(() => {
  try {
    if (sources) for (const [name, bytes] of sources) {
      assert.ok(regularBytes(root, name).equals(bytes), `Selected source mutated during tests: ${name}`);
    }
    if (policies) assert.deepEqual(digestRows(policySnapshot()), digestRows(policies), "Repository policy mutated during tests.");
    if (controls) for (const [name, bytes] of controls) {
      assert.ok(regularBytes(packet, name).equals(bytes), `Frozen packet mutated during tests: ${name}`);
    }
  } finally {
    if (owned) {
      assert.equal(dirname(owned), ownedParent);
      assert.equal(realpathSync(owned), owned);
      assert.equal(regularBytes(owned, "OWNER").toString("utf8"), owner, "Refusing cleanup without ownership.");
      rmSync(owned, { recursive: true, force: false, maxRetries: 2, retryDelay: 100 });
    }
  }
});

function fixture(autocrlf) {
  const repo = ensureDirectory(owned, `fixture-${++fixtureNumber}`);
  const home = join(owned, "home");
  const empty = join(owned, "empty-config");
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) =>
    /^(PATH|PATHEXT|SYSTEMROOT|WINDIR|COMSPEC)$/i.test(key)));
  Object.assign(env, {
    HOME: home, USERPROFILE: home, XDG_CONFIG_HOME: home, APPDATA: home, LOCALAPPDATA: home,
    TEMP: owned, TMP: owned, TMPDIR: owned,
    GIT_CONFIG_NOSYSTEM: "1", GIT_CONFIG_GLOBAL: empty, GIT_ATTR_NOSYSTEM: "1",
    GIT_TERMINAL_PROMPT: "0", GIT_ALLOW_PROTOCOL: "", LC_ALL: "C",
  });
  function git(args, { input, statuses = [0], checkout } = {}) {
    const result = spawnSync("git", [
      "--no-pager", "-C", repo,
      "-c", `core.autocrlf=${autocrlf}`, "-c", "core.safecrlf=false",
      "-c", `core.attributesFile=${empty}`, "-c", `core.excludesFile=${empty}`,
      "-c", `core.hooksPath=${join(owned, "hooks")}`,
      ...(checkout ? [`--work-tree=${checkout}`] : []), ...args,
    ], { env, input, cwd: repo, windowsHide: true, timeout: 30_000, maxBuffer: 2 * 1024 * 1024 });
    if (result.error) throw result.error;
    assert.ok(!result.signal && statuses.includes(result.status),
      `Native git ${args[0]} failed (${result.status}): ${result.stderr.toString("utf8")}`);
    return result;
  }
  git(["init", "--quiet", `--template=${join(owned, "empty-template")}`, "--initial-branch=publication-fixture"]);
  assert.equal(git(["config", "--get", "core.autocrlf"]).stdout.toString("utf8").trim(), String(autocrlf));
  return { repo, git };
}

function copyPolicies(repo, suffix) {
  for (const [name, bytes] of policies) if (name.endsWith(suffix)) put(repo, name, bytes);
}

function roundtrip(current, inputs) {
  for (const [name, bytes] of inputs) put(current.repo, name, bytes);
  const filters = current.git(["check-attr", "-z", "filter", "--", ...inputs.keys()]).stdout.toString("utf8").split("\0");
  assert.equal(filters.pop(), "");
  assert.equal(filters.length, inputs.size * 3);
  for (let index = 0; index < filters.length; index += 3) {
    assert.ok(["unspecified", "unset"].includes(filters[index + 2]), `No conversion filters allowed: ${filters[index]}`);
  }
  current.git(["add", "--", ...inputs.keys()]);
  const checkout = ensureDirectory(owned, `checkout-${fixtureNumber}`);
  current.git(["checkout-index", "--all", "--force"], { checkout });
  return new Map([...inputs].map(([name]) => [name, {
    blob: current.git(["cat-file", "blob", `:${name}`]).stdout,
    checkout: regularBytes(checkout, name),
  }]));
}

function ignoreRows(current, paths, noIndex = true) {
  const result = current.git([
    "check-ignore", ...(noIndex ? ["--no-index"] : []),
    "--verbose", "--non-matching", "-z", "--stdin",
  ], { input: Buffer.from(`${paths.join("\0")}\0`), statuses: [0, 1] });
  const fields = result.stdout.toString("utf8").split("\0");
  assert.equal(fields.pop(), "");
  assert.equal(fields.length, paths.length * 4, "Native check-ignore must report every selected probe.");
  return paths.map((path, index) => {
    const [source, line, pattern, actualPath] = fields.slice(index * 4, index * 4 + 4);
    assert.equal(actualPath, path);
    return { path, ignored: pattern !== "" && !pattern.startsWith("!"), source, line, pattern };
  });
}

test("native Git LF/CRLF calibration and frozen input controls", (t) => {
  t.diagnostic(JSON.stringify({ kind: "policy-before-run", files: digestRows(policies) }));
  t.diagnostic(JSON.stringify({ kind: "control-before-run", files: digestRows(controls) }));
  const lf = Buffer.from("publication fixture\nsecond line\n");
  const crlf = Buffer.from("publication fixture\r\nsecond line\r\n");
  for (const autocrlf of [false, true]) {
    const current = fixture(autocrlf);
    t.diagnostic(JSON.stringify({ kind: "git-version", autocrlf, version: current.git(["--version"]).stdout.toString("utf8").trim() }));
    // Calibration policy is separate; it is never merged into the repository-policy roundtrip.
    const inputs = new Map([
      [".gitattributes", Buffer.from("forced-*.txt text eol=lf\nraw-*.txt -text\n")],
      ["forced-lf.txt", lf], ["forced-crlf.txt", crlf],
      ["raw-lf.txt", lf], ["raw-crlf.txt", crlf],
      ["automatic-lf.txt", lf], ["automatic-crlf.txt", crlf],
    ]);
    const results = roundtrip(current, inputs);
    for (const [name, input] of [...inputs].slice(1)) {
      const expectedBlob = name.startsWith("forced-") || (name.startsWith("automatic-") && autocrlf) ? lf : input;
      const expectedCheckout = name.startsWith("forced-") ? lf
        : name.startsWith("automatic-") && autocrlf ? crlf : input;
      assert.ok(results.get(name).blob.equals(expectedBlob), `Native calibration blob: ${name}, autocrlf=${autocrlf}`);
      assert.ok(results.get(name).checkout.equals(expectedCheckout), `Native calibration checkout: ${name}, autocrlf=${autocrlf}`);
      t.diagnostic(JSON.stringify({ kind: "calibration", autocrlf, path: name,
        sourceSha256: sha256(input), blobSha256: sha256(results.get(name).blob),
        checkoutSha256: sha256(results.get(name).checkout), passed: true }));
    }
  }
});

test("selected protected inputs retain exact blob and clean checkout bytes", (t) => {
  const failures = [];
  for (const autocrlf of [false, true]) {
    const current = fixture(autocrlf);
    const inputs = new Map([...policies].filter(([name]) => name.endsWith(".gitattributes")));
    for (const [name, bytes] of sources) inputs.set(name, bytes);
    const results = roundtrip(current, inputs);
    for (const [name, original] of sources) {
      const actual = results.get(name);
      const row = { kind: "exact-byte-roundtrip", autocrlf, path: name, sourceSha256: sha256(original),
        blobSha256: sha256(actual.blob), checkoutSha256: sha256(actual.checkout),
        blobExact: actual.blob.equals(original), checkoutExact: actual.checkout.equals(original) };
      t.diagnostic(JSON.stringify(row));
      if (!row.blobExact || !row.checkoutExact) failures.push({
        path: name, autocrlf, blobExact: row.blobExact, checkoutExact: row.checkoutExact,
      });
    }
  }
  assert.deepEqual(failures, [], `Exact-byte publication mismatch:\n${JSON.stringify(failures, null, 2)}`);
});

test("mutable generated paths are ignored by copied repository policy", (t) => {
  const current = fixture(false);
  copyPolicies(current.repo, ".gitignore");
  for (const name of mustIgnore) put(current.repo, name, "synthetic publication path probe only\n");
  const rows = ignoreRows(current, mustIgnore);
  for (const row of rows) t.diagnostic(JSON.stringify({ kind: "generated-ignore", ...row }));
  assert.deepEqual(rows.filter((row) => !row.ignored).map((row) => row.path), [],
    "Mutable publication paths remain eligible for accidental staging.");
});

test("sources, licenses, evidence and explicitly tracked historical RED remain versionable", (t) => {
  const current = fixture(false);
  for (const name of trackedHistory) put(current.repo, name, "synthetic historical RED selection\n");
  current.git(["add", "--", ...trackedHistory]);
  copyPolicies(current.repo, ".gitignore");
  for (const name of mustKeep) put(current.repo, name, "synthetic publication source path probe only\n");
  const rows = ignoreRows(current, mustKeep);
  for (const row of rows) t.diagnostic(JSON.stringify({ kind: "source-visible", ...row }));
  for (const row of ignoreRows(current, trackedHistory)) {
    t.diagnostic(JSON.stringify({ kind: "historical-default-ignore-informational", ...row }));
  }
  assert.deepEqual(ignoreRows(current, trackedHistory, false).filter((row) => row.ignored), [],
    "Copied ignore policy must not hide already indexed selections.");
  for (const name of trackedHistory) {
    const updated = Buffer.from("synthetic historical RED selection\nexplicit tracked update\n");
    writeFileSync(native(current.repo, name), updated);
    current.git(["add", "--", name]);
    assert.ok(current.git(["cat-file", "blob", `:${name}`]).stdout.equals(updated));
    assert.equal(current.git(["ls-files", "--error-unmatch", "--", name]).stdout.toString("utf8").trim(), name);
  }
  assert.deepEqual(rows.filter((row) => row.ignored).map((row) => row.path), [],
    "Publication policy must not hide source, fixtures, licenses, oracle material or evidence.");
});
