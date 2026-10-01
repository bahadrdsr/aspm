import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const origin = "21080a3cbe04957e4ff55a6236d13bb4fde0fcc2";
const closureSHA256 = "57acf410cb798843f3dc1b09b1c6a83ecbfd2ff260118d9c75a1960b04f5b6a6";
const originalHelperSHA256 = "40d4acf4b14ed7045f1030353eaa51ac8dd43ac49d651836e6847221c8ef2825";
const hash = (data) => createHash("sha256").update(data).digest("hex");
const label = process.argv[2];
assert.match(label ?? "", /^[a-z0-9]+(?:-[a-z0-9]+)*$/, "Fresh bounded build label required");
const cache = join(root, ".cache", "teams-workflows", "published-v10", label);
const output = join(root, ".artifacts", "teams-workflows");
const work = join(root, ".cache", "teams-workflows", "work");
assert.ok(!existsSync(cache), "Refusing to replace a pinned build");
const git = (...args) => execFileSync("git", ["--no-pager", ...args], { cwd: root, maxBuffer: 4 << 20 });
const entries = git("ls-tree", "-r", "--format=%(objectname)%x09%(path)", origin).toString().trim().split(/\r?\n/)
  .map((line) => { const [gitBlob, path] = line.split("\t"); return { gitBlob, path }; })
  .filter(({ path }) => path === "go.mod" || path === "go.sum" ||
    /^internal\/(app|parsers|providers|connectors|evidence)\/.*\.go$/.test(path) && !path.endsWith("_test.go"))
  .map((entry) => ({ ...entry, path: entry.path.replaceAll("/", "\\") }))
  .sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
assert.equal(entries.length, 86, "Pinned production closure changed");
assert.equal(hash(entries.map(({ path, gitBlob }) => `${path}\0${gitBlob}\n`).join("")), closureSHA256,
  "Pinned path/blob identities changed");
for (const path of [cache, output, work]) mkdirSync(path, { recursive: true });
const files = entries.map(({ path, gitBlob }) => {
  const bytes = git("cat-file", "blob", gitBlob);
  const target = join(cache, ...path.split("\\"));
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, bytes, { flag: "wx" });
  return { path, gitBlob, sha256: hash(bytes), bytes: bytes.length };
});
assert.ok(files.reduce((sum, file) => sum + file.bytes, 0) < (2 << 20), "Pinned closure size exceeded");
const helper = readFileSync(join(root, "tests", "jira_work_items", "testdata", "published-core.main.go.txt"));
const workerHelper = readFileSync(join(root, "tests", "jira_work_items", "teams", "published-worker.main.go.txt"));
assert.equal(hash(helper), originalHelperSHA256, "Unchanged real-HTTP core helper drifted");
const command = join(cache, "cmd", "owned-v10-core");
mkdirSync(command, { recursive: true });
writeFileSync(join(command, "main.go"), helper, { flag: "wx" });
writeFileSync(join(command, "worker.go"), workerHelper, { flag: "wx" });
const executable = join(cache, "owned-v10-core.exe");
const go = join(root, ".cache", "modules", "golang.org", "toolchain@v0.0.1-go1.27.1.windows-amd64", "bin", "go.exe");
assert.ok(existsSync(go), "Cached Go1.27.1 required; no install or restore");
const env = { ...process.env };
for (const name of Object.keys(env)) {
  if (/^(ASPM_|ASMP_|TEAMS_|JIRA_|SLACK_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$/i.test(name)) delete env[name];
}
Object.assign(env, {
  GOTOOLCHAIN: "local", GOENV: "off", GOWORK: "off", GOFLAGS: "", GOPROXY: "off", GOSUMDB: "off",
  GOMODCACHE: join(root, ".cache", "modules"), GOCACHE: join(root, ".cache", "build"),
  GOPATH: join(root, ".cache", "teams-workflows", "gopath"), GOTMPDIR: work, TEMP: work, TMP: work, TMPDIR: work,
});
const args = ["build", "-mod=readonly", "-o", executable, ".\\cmd\\owned-v10-core"];
const result = spawnSync(go, args, { cwd: cache, env, encoding: "utf8", timeout: 120_000, maxBuffer: 4 << 20 });
writeFileSync(join(output, `${label}.published-v10-build.log`), `${result.stdout ?? ""}${result.stderr ?? ""}`, { flag: "wx" });
if (result.error || result.status !== 0) {
  console.error("Pinned V10 build failed; bounded log retained, no dependencies installed.");
  process.exit(result.status ?? 1);
}
const receipt = {
  origin, executable, executableSHA256: hash(readFileSync(executable)), closureSHA256, files,
  helperSHA256: hash(helper), workerHelperSHA256: hash(workerHelper), arguments: args,
  businessSQLSeeds: false, providerCalls: false, dataExecution: false, exitCode: 0,
};
writeFileSync(join(output, `${label}.published-v10-build.json`), JSON.stringify(receipt, null, 2) + "\n", { flag: "wx" });
console.log("Exact published V10 production closure compiled with HTTP/worker forwarding helpers; no data execution.");
