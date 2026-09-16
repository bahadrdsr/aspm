import assert from "node:assert/strict";
import { createHash, createPrivateKey, createPublicKey, generateKeyPairSync, sign, verify } from "node:crypto";
import { readFileSync, realpathSync, statSync, writeFileSync } from "node:fs";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { root } from "./process.mjs";

function git(args) {
  const result = spawnSync("git", args, { cwd: root, encoding: "utf8" });
  if (result.error) throw result.error;
  assert.equal(result.status, 0, "Git source identity could not be checked.");
  return result.stdout.trim();
}
function inspectManifest(file) {
  const bytes = readFileSync(file);
  const manifest = JSON.parse(bytes);
  assert.equal(manifest.kind, "DevelopmentArtifactManifest");
  assert.equal(manifest.productionReady, false);
  const base = realpathSync(dirname(file));
  for (const entry of manifest.files) {
    assert.equal(isAbsolute(entry.path), false);
    const parts = entry.path.split("/");
    assert.ok(parts.every((part) => /^[A-Za-z0-9_.-]+$/.test(part) && part !== "." && part !== ".."));
    const path = realpathSync(resolve(base, ...parts));
    const within = relative(base, path);
    assert.ok(within !== ".." && !within.startsWith(`..${process.platform === "win32" ? "\\" : "/"}`) && !isAbsolute(within));
    assert.equal(createHash("sha256").update(readFileSync(path)).digest("hex"), entry.sha256, `Artifact changed: ${entry.path}`);
  }
  return { bytes, manifest };
}
const [action, input, publicKeyFile] = process.argv.slice(2);
if (action === "self-test") {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const message = Buffer.from('{"data":"synthetic","purpose":"M01 signing fixture"}');
  const signature = sign(null, message, privateKey);
  assert.ok(verify(null, message, publicKey, signature));
  assert.equal(verify(null, Buffer.concat([message, Buffer.from("changed")]), publicKey, signature), false);
  console.log("Controlled in-memory Ed25519 signature and tamper checks passed; no release key or artifact was published.");
} else {
  assert.ok(input, "Supply an artifact manifest path.");
  const file = resolve(input);
  const { bytes, manifest } = inspectManifest(file);
  if (action === "sign") {
    const approved = process.env.ASPM_RELEASE_APPROVED_REVISION;
    assert.match(approved ?? "", /^[a-f0-9]{40}$/, "Protected reviewed-revision approval is required.");
    assert.equal(git(["rev-parse", "HEAD"]), approved, "Approval does not bind this checkout.");
    assert.equal(git(["status", "--porcelain"]), "", "Do not sign an unreviewed or dirty checkout.");
    assert.equal(manifest.sourceRevision, approved);
    assert.equal(manifest.sourceDirty, false);
    const keyPath = process.env.ASPM_SIGNING_KEY_FILE;
    assert.ok(keyPath, "Provide a protected key-file reference, never a key in argv.");
    if (process.platform !== "win32") assert.equal(statSync(keyPath).mode & 0o077, 0, "Signing key permissions must be owner-only.");
    const key = createPrivateKey(readFileSync(keyPath));
    assert.equal(key.asymmetricKeyType, "ed25519");
    writeFileSync(`${file}.sig`, sign(null, bytes, key).toString("base64") + "\n");
    console.log(`Signed manifest: ${join(dirname(file), "artifact-manifest.json.sig")}. No registry push performed.`);
  } else {
    assert.equal(action, "verify", "Use sign, verify or self-test.");
    assert.ok(publicKeyFile, "Verification requires an independently trusted public-key file.");
    const key = createPublicKey(readFileSync(publicKeyFile));
    assert.equal(key.asymmetricKeyType, "ed25519");
    assert.ok(verify(null, bytes, key, Buffer.from(readFileSync(`${file}.sig`, "utf8").trim(), "base64")), "Signature verification failed.");
    console.log("Manifest signature and every listed artifact digest verified against the supplied trusted public key.");
  }
}
