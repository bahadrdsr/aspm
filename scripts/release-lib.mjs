import assert from "node:assert/strict";
import { createHash, createPrivateKey, createPublicKey, sign, verify } from "node:crypto";
import { gunzipSync, gzipSync } from "node:zlib";

export const digestHex = (bytes) => createHash("sha256").update(bytes).digest("hex");
export const digest = (bytes) => `sha256:${digestHex(bytes)}`;
export const canonicalJSON = (value) => Buffer.from(`${JSON.stringify(value, null, 2)}\n`);

const releasePattern = /^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*)?$/;
const imagePattern = /^[A-Za-z0-9][A-Za-z0-9._:/-]*:[A-Za-z0-9_][A-Za-z0-9_.-]*@sha256:[a-f0-9]{64}$/;
const baseImagePattern = /^[A-Za-z0-9][A-Za-z0-9._:/-]*:[A-Za-z0-9_][A-Za-z0-9_.-]*@sha256:[a-f0-9]{64}$/;

export function validateReleaseConfiguration(config) {
  assert.deepEqual(Object.keys(config).sort(), [
    "apiVersion", "baseImages", "imageRepository", "kind", "platform",
    "postgresImage", "storageImage", "version",
  ]);
  assert.equal(config.apiVersion, "aspm.dev/release/v1alpha1");
  assert.equal(config.kind, "ReleaseConfiguration");
  assert.match(config.version, releasePattern);
  assert.equal(config.platform, "linux/amd64");
  assert.match(config.imageRepository, /^[A-Za-z0-9][A-Za-z0-9._/-]*$/);
  assert.match(config.postgresImage, imagePattern);
  assert.match(config.storageImage, imagePattern);
  assert.deepEqual(Object.keys(config.baseImages).sort(), ["runtime"]);
  for (const value of Object.values(config.baseImages)) assert.match(value, baseImagePattern);
  return config;
}

function octal(value, length) {
  const text = value.toString(8);
  assert.ok(text.length <= length - 1, `Tar value ${value} exceeds ${length} bytes.`);
  return `${text.padStart(length - 1, "0")}\0`;
}

function tarHeader(entry) {
  assert.match(entry.path, /^(?!\/)(?!.*(?:^|\/)\.\.?\/)[A-Za-z0-9_@.-]+(?:\/[A-Za-z0-9_@.-]+)*$/);
  const name = Buffer.from(entry.path);
  assert.ok(name.length <= 100, `Tar path is too long: ${entry.path}`);
  const header = Buffer.alloc(512);
  name.copy(header, 0);
  Buffer.from(octal(entry.mode ?? 0o644, 8)).copy(header, 100);
  Buffer.from(octal(0, 8)).copy(header, 108);
  Buffer.from(octal(0, 8)).copy(header, 116);
  Buffer.from(octal(entry.bytes.length, 12)).copy(header, 124);
  Buffer.from(octal(0, 12)).copy(header, 136);
  Buffer.from("        ").copy(header, 148);
  header[156] = "0".charCodeAt(0);
  Buffer.from("ustar\0").copy(header, 257);
  Buffer.from("00").copy(header, 263);
  let sum = 0;
  for (const value of header) sum += value;
  Buffer.from(`${sum.toString(8).padStart(6, "0")}\0 `).copy(header, 148);
  return header;
}

export function deterministicTarGzip(entries) {
  const sorted = [...entries].sort((left, right) => left.path < right.path ? -1 : left.path > right.path ? 1 : 0);
  assert.equal(new Set(sorted.map((entry) => entry.path)).size, sorted.length, "Tar paths must be unique.");
  const chunks = [];
  for (const entry of sorted) {
    assert.ok(Buffer.isBuffer(entry.bytes));
    chunks.push(tarHeader(entry), entry.bytes);
    const padding = (512 - (entry.bytes.length % 512)) % 512;
    if (padding) chunks.push(Buffer.alloc(padding));
  }
  chunks.push(Buffer.alloc(1024));
  const output = gzipSync(Buffer.concat(chunks), { level: 9, mtime: 0 });
  output[9] = 255;
  return output;
}

function tarString(header, start, length) {
  const end = header.indexOf(0, start);
  return header.subarray(start, end < 0 || end > start + length ? start + length : end).toString("utf8");
}

function tarOctal(header, start, length) {
  const text = tarString(header, start, length).trim();
  return text === "" ? 0 : Number.parseInt(text, 8);
}

export function readTar(data) {
  const result = new Map();
  let offset = 0;
  while (offset + 512 <= data.length) {
    const header = data.subarray(offset, offset + 512);
    if (header.every((value) => value === 0)) break;
    const expected = tarOctal(header, 148, 8);
    const copy = Buffer.from(header);
    Buffer.from("        ").copy(copy, 148);
    let actual = 0;
    for (const value of copy) actual += value;
    assert.equal(actual, expected, "Tar header checksum mismatch.");
    const name = tarString(header, 0, 100);
    const prefix = tarString(header, 345, 155);
    const type = String.fromCharCode(header[156] || "0".charCodeAt(0));
    const rawPath = prefix ? `${prefix}/${name}` : name;
    const path = type === "5" ? rawPath.replace(/\/+$/, "") : rawPath;
    assert.match(path, /^(?!\/)(?!.*(?:^|\/)\.\.?\/)[A-Za-z0-9_@.-]+(?:\/[A-Za-z0-9_@.-]+)*$/);
    const size = tarOctal(header, 124, 12);
    offset += 512;
    assert.ok(offset + size <= data.length, "Tar member exceeds archive.");
    if (type === "0" || type === "\0") {
      assert.ok(!result.has(path), `Duplicate tar member: ${path}`);
      result.set(path, Buffer.from(data.subarray(offset, offset + size)));
    } else {
      assert.equal(type, "5", `Unsupported tar member type ${type}.`);
    }
    offset += Math.ceil(size / 512) * 512;
  }
  return result;
}

export function readTarGzip(archive) {
  return readTar(gunzipSync(archive));
}

export function privateKey(value) {
  const key = value?.type === "private" ? value : createPrivateKey(value);
  assert.equal(key.asymmetricKeyType, "ed25519");
  return key;
}

export function publicKey(value) {
  const key = value?.type === "public" ? value : createPublicKey(value);
  assert.equal(key.asymmetricKeyType, "ed25519");
  return key;
}

export function signBytes(bytes, key) {
  const signature = sign(null, bytes, key);
  assert.equal(signature.length, 64);
  return signature;
}

export function verifyBytes(bytes, signature, key) {
  assert.equal(signature.length, 64);
  assert.ok(verify(null, bytes, key, signature), "Ed25519 signature verification failed.");
}
