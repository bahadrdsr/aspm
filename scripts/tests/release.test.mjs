import assert from "node:assert/strict";
import { generateKeyPairSync } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import {
  canonicalJSON, deterministicTarGzip, privateKey, publicKey, readTarGzip,
  signBytes, validateReleaseConfiguration, verifyBytes,
} from "../release-lib.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");

test("technical preview release configuration is exact and digest pinned", () => {
  const config = validateReleaseConfiguration(JSON.parse(
    readFileSync(join(root, "release", "technical-preview.json"), "utf8"),
  ));
  assert.equal(config.version, "0.1.0-rc.1");
  assert.equal(config.imageRepository, "localhost/aspm");
  for (const value of [
    config.postgresImage, config.storageImage, ...Object.values(config.baseImages),
  ]) assert.match(value, /@sha256:[a-f0-9]{64}$/);
  const containerfile = readFileSync(join(root, "release", "Containerfile"), "utf8");
  for (const value of Object.values(config.baseImages)) assert.ok(containerfile.includes(value));
  assert.ok(containerfile.includes("ARG ASPM_VERSION=0.1.0-dev.1"));
  assert.ok(containerfile.includes("COPY bin/ /app/bin/"));
  assert.ok(containerfile.includes("COPY web/ /app/web/"));
  assert.ok(!containerfile.includes("npm ci"));
  assert.ok(!containerfile.includes("go mod download"));
});

test("release archives are byte deterministic and path bounded", () => {
  const entries = [
    { path: "aspm/bin/aspmctl", bytes: Buffer.from("binary\n"), mode: 0o755 },
    { path: "aspm/NOTICE", bytes: Buffer.from("notice\n") },
  ];
  const first = deterministicTarGzip(entries);
  const second = deterministicTarGzip([...entries].reverse());
  assert.deepEqual(first, second);
  const extracted = readTarGzip(first);
  assert.deepEqual([...extracted.keys()], ["aspm/NOTICE", "aspm/bin/aspmctl"]);
  assert.deepEqual(extracted.get("aspm/bin/aspmctl"), Buffer.from("binary\n"));
  assert.throws(() => deterministicTarGzip([{ path: "../escape", bytes: Buffer.alloc(0) }]));
});

test("release signatures are detached Ed25519 exact-byte bindings", () => {
  const pair = generateKeyPairSync("ed25519");
  const bytes = canonicalJSON({ kind: "SyntheticReleaseManifest", files: [] });
  const signature = signBytes(bytes, privateKey(pair.privateKey));
  verifyBytes(bytes, signature, publicKey(pair.publicKey));
  assert.throws(() => verifyBytes(Buffer.concat([bytes, Buffer.from("changed")]), signature, publicKey(pair.publicKey)));
});

test("manual release workflow retains protected approval and external trust", () => {
  const workflow = readFileSync(join(root, ".woodpecker", "release.yaml"), "utf8");
  for (const required of [
    "ASPM_RELEASE_APPROVED_REVISION", "ASPM_SIGNING_KEY_FILE",
    "ASPM_RELEASE_TRUSTED_KEY_FILE", "scripts/release.mjs prepare",
    "scripts/release.mjs finalize", "scripts/release.mjs verify",
    "docker buildx version",
  ]) assert.ok(workflow.includes(required), `Release workflow omitted ${required}.`);
  assert.ok(!/docker\s+push|oras\s+push|gh\s+release/.test(workflow),
    "Release workflow must not publish without a separate operator action.");
});
