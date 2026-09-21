import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../", import.meta.url));
const label = process.argv[2];
if (!label || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(label)) throw new Error("Fresh bounded fixture-build label required.");
const manifestPath = join(root, "tests", "ai_assessments", "testdata", "published-v7-source-inputs.json");
const manifest = JSON.parse(readFileSync(manifestPath));
assert.equal(manifest.originCommit, "98a41ced71c09e71c8cc2bd158678fd52268498c");
const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const cache = join(root, ".cache", "ai-assessments-v1", "published-v7", label);
const output = join(root, ".artifacts", "ai-assessments-v1");
const work = join(root, ".cache", "ai-assessments-v1", "work");
for (const directory of [cache, output, work]) mkdirSync(directory, { recursive: true });
for (const entry of manifest.files) {
  assert.ok(!entry.path.includes("..") && !entry.path.startsWith("\\") && !entry.path.includes(":"));
  const bytes = execFileSync("git", ["show", `${manifest.originCommit}:${entry.path.replaceAll("\\", "/")}`], { cwd: root, maxBuffer: 16 << 20 });
  assert.equal(bytes.length, entry.bytes);
  assert.equal(hash(bytes), entry.sha256, entry.path);
  const target = join(cache, entry.path);
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, bytes);
}
const seedPath = join(root, "tests", "ai_assessments", "testdata", "seed-v7.main.go.txt");
const main = join(cache, "cmd", "owned-v7-seed", "main.go");
mkdirSync(dirname(main), { recursive: true });
writeFileSync(main, readFileSync(seedPath));
const executable = join(cache, "owned-v7-seed.exe");
const go = join(root, ".cache", "modules", "golang.org", "toolchain@v0.0.1-go1.27.1.windows-amd64", "bin", "go.exe");
const env = { ...process.env };
for (const name of Object.keys(env)) {
  if (/^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$/i.test(name)) delete env[name];
}
Object.assign(env, {
  GOTOOLCHAIN: "local", GOENV: "off", GOWORK: "off", GOFLAGS: "", GOPROXY: "off", GOSUMDB: "off",
  GOMODCACHE: join(root, ".cache", "modules"), GOCACHE: join(root, ".cache", "build"),
  GOPATH: join(root, ".cache", "ai-assessments-v1", "gopath"), GOTMPDIR: work, TEMP: work, TMP: work, TMPDIR: work,
});
const startedAt = new Date().toISOString();
const result = spawnSync(go, ["build", "-mod=readonly", "-o", executable, ".\\cmd\\owned-v7-seed"], {
  cwd: cache, env, encoding: "utf8", timeout: 120_000, maxBuffer: 4 << 20,
});
const log = `${result.stdout ?? ""}${result.stderr ?? ""}`;
writeFileSync(join(output, `${label}.published-v7-build.log`), log, { flag: "wx" });
if (result.error || result.status !== 0) {
  console.error(log || "Pinned published V7 fixture build failed; no install attempted.");
  process.exit(result.status ?? 1);
}
const binary = readFileSync(executable);
const receipt = {
  schemaVersion: 1, startedAt, finishedAt: new Date().toISOString(), origin: manifest.originCommit,
  sourceFiles: manifest.files.length, sourceManifestSHA256: hash(readFileSync(manifestPath)),
  seedHelperSHA256: hash(readFileSync(seedPath)), executable, executableSHA256: hash(binary), executableBytes: binary.length,
  boundary: "Exact pinned production dependency closure plus an API/intake-only fixture helper. No old test/review history copied, business-row SQL insertion, native model call or deployment.",
  externalDependenciesInstalled: false, exitCode: 0,
};
writeFileSync(join(output, `${label}.published-v7-build.json`), JSON.stringify(receipt, null, 2) + "\n", { flag: "wx" });
console.log(`Pinned V7 API/intake fixture built from ${manifest.files.length} exact published source inputs; not assessment acceptance.`);
