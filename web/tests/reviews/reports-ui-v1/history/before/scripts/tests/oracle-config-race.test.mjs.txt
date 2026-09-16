import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import fs from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { withOracleGuard } from "../oracle-lib.mjs";

const repository = fileURLToPath(new URL("../../", import.meta.url));
const hash = (bytes) => `sha256:${createHash("sha256").update(bytes).digest("hex")}`;

function setup(t) {
  const parent = join(repository, ".cache", "oracle-config-race");
  fs.mkdirSync(parent, { recursive: true });
  const root = fs.mkdtempSync(join(parent, "case-"));
  const configPath = join(root, "config.json");
  const config = (name) => Buffer.from(JSON.stringify({
    schemaVersion: 1, oracles: [{ manifest: `${name}-oracle.json`, signature: `${name}-oracle.sig` }],
    requiredControls: ["guard.mjs"],
  }));
  const checked = config("good");
  const changed = config("evil");
  assert.equal(checked.length, changed.length);
  fs.writeFileSync(configPath, checked);
  const originals = { openSync: fs.openSync, readSync: fs.readSync, closeSync: fs.closeSync, lstatSync: fs.lstatSync };
  let opens = 0;
  const reads = new Map();
  const paths = [];
  t.mock.method(fs, "openSync", (path, ...args) => {
    const fd = originals.openSync(path, ...args);
    if (resolve(String(path)) === configPath) {
      opens += 1;
      reads.set(fd, { bytes: opens === 1 ? checked : changed, position: 0 });
    }
    return fd;
  });
  t.mock.method(fs, "readSync", (fd, buffer, offset, length, position) => {
    const input = reads.get(fd);
    if (!input) return originals.readSync(fd, buffer, offset, length, position);
    const size = Math.min(length, input.bytes.length - input.position);
    input.bytes.copy(buffer, offset, input.position, input.position + size);
    input.position += size;
    return size;
  });
  t.mock.method(fs, "closeSync", (fd) => {
    reads.delete(fd);
    return originals.closeSync(fd);
  });
  t.mock.method(fs, "lstatSync", (path, ...args) => {
    const normalized = resolve(String(path));
    if (dirname(normalized) === root && /(?:good|evil)-oracle\.json$/.test(normalized)) paths.push(normalized);
    return originals.lstatSync(path, ...args);
  });
  syncBuiltinESMExports();
  t.after(() => {
    t.mock.restoreAll();
    syncBuiltinESMExports();
    fs.rmSync(root, { recursive: true, force: true });
  });
  return {
    root, configPath, checked, changed, paths, opens: () => opens,
    options: {
      root, configPath, configDigest: hash(checked),
      trustedKeyPath: join(root, "unused-public-authority.pem"),
      trustedKeyFingerprint: `sha256:${"1".repeat(64)}`,
      files: ["guard.mjs"],
    },
  };
}

test("configuration path selection uses the exact digest-checked buffer, not a second file read", (t) => {
  const f = setup(t);
  let called = false;
  // Stop at the selected missing manifest, before keys, signatures or commands.
  assert.throws(() => withOracleGuard(f.options, () => { called = true; }), /missing|inaccessible/);
  assert.equal(f.opens(), 1, "Pinned configuration must be read once for hashing and parsing.");
  assert.ok(f.paths.includes(join(f.root, "good-oracle.json")), "The authenticated configuration's selector was not used.");
  assert.ok(!f.paths.includes(join(f.root, "evil-oracle.json")), "Unpinned replacement bytes selected an oracle path.");
  assert.equal(called, false);
});

test("mismatching configuration bytes cannot reach manifest paths or the guarded command", (t) => {
  const f = setup(t);
  let called = false;
  assert.throws(() => withOracleGuard({ ...f.options, configDigest: hash(f.changed) }, () => { called = true; }), /digest mismatch/);
  assert.equal(f.opens(), 1);
  assert.deepEqual(f.paths, []);
  assert.equal(called, false);
});
