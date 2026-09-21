import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const review = dirname(fileURLToPath(import.meta.url));
const root = resolve(review, "..", "..", "..", "..");
const tests = join(root, "tests", "assessment_deployment");
const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const baselineFile = join(review, "BEFORE.json");
assert.equal(hash(readFileSync(baselineFile)), "f7db6471ff8e985cf0f4005d9c6f9473767edb04b2c0bb185e9e5e2ed097c200");
const baseline = JSON.parse(readFileSync(baselineFile));
const witness = (path) => {
  const bytes = readFileSync(join(root, path));
  return { path, bytes: bytes.length, sha256: hash(bytes) };
};
const fixed = baseline.files.filter((entry) => entry.path.startsWith("tests\\") || entry.path.startsWith(".oracles\\") ||
  ["go.mod", "go.sum", "internal\\install\\execution\\linux.go", "internal\\install\\execution\\bundle.go"].includes(entry.path));
for (const entry of fixed) assert.equal(witness(entry.path).sha256, entry.sha256, `Old control/module/installer drift: ${entry.path}`);
for (const entry of baseline.toolWitnesses) assert.equal(hash(readFileSync(entry.path)), entry.sha256, `Selected tool/cache drift: ${entry.path}`);
const current = execFileSync("git", ["--no-pager", "ls-files", "--cached", "--others", "--exclude-standard"], { cwd: root, encoding: "utf8" })
  .split(/\r?\n/).filter(Boolean).map((path) => path.replaceAll("/", "\\"))
  .filter((path) => !path.startsWith("tests\\assessment_deployment\\") && !/(^|\\)(\.env$|[^\\]*private[^\\]*\.(pem|key)$)/i.test(path));
const operation = process.argv[2];
const currentFiles = [...new Set(current)].sort().map(witness);
if (operation === "author-check") {
  assert.deepEqual(currentFiles.map((entry) => entry.path), baseline.files.map((entry) => entry.path).sort(), "Author changed existing source/control file set");
  for (const entry of baseline.files) assert.equal(witness(entry.path).sha256, entry.sha256, `Author changed old source/control: ${entry.path}`);
  for (const absent of baseline.initiallyMissing) assert.equal(existsSync(join(root, absent)), false, "Author created production artifact");
  console.log(`Author preservation verified: ${baseline.files.length} existing files unchanged; no producer edit.`);
  process.exit(0);
}
const output = resolve(process.argv[3] ?? "");
const relativeOutput = relative(join(root, ".artifacts", "assessment-deployment-v1"), output);
assert.ok(relativeOutput && !relativeOutput.startsWith("..") && !relativeOutput.includes("\\") && !relativeOutput.includes("/"), "Only a selected owned run directory is allowed");
assert.ok(["before", "after"].includes(operation));
const authored = [
  ...readdirSync(tests).filter((name) => !name.startsWith(".") && /\.(go|mod|sum|ps1|txt)$/.test(name)).map((name) => relative(root, join(tests, name))),
  relative(root, baselineFile), relative(root, fileURLToPath(import.meta.url)),
].map(witness);
const sourceTree = hash(JSON.stringify(currentFiles));
if (operation === "after") {
  const before = JSON.parse(readFileSync(join(output, "source-before.json")));
  assert.deepEqual(before.authoredInputs, authored, "Authored test input changed while executing");
  assert.deepEqual(before.files, currentFiles, "Current producer/control input changed DURING this actual run");
  assert.equal(before.sourceTreeSHA256, sourceTree);
}
writeFileSync(join(output, `source-${operation}.json`), JSON.stringify({
  schemaVersion: 1, phase: operation, capturedAt: new Date().toISOString(),
  sourceTreeSHA256: sourceTree, existingSourceControlCount: currentFiles.length, files: currentFiles,
  oldTestsModulesInstallerSignaturesUnchanged: true, currentSourceStableDuringRun: true,
  producerEditAuthorization: "None supplied by this recorder; parent source guard/classification remains authoritative, including scripts.",
  authoredInputs: authored,
  privateRuntimeOrCredentialFilesRead: false,
}, null, 2) + "\n", { flag: "wx" });
console.log(`Verified ${baseline.files.length} current source/control files and selected cached tools; phase=${operation}.`);
