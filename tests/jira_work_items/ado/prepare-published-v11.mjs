import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const origin = "69f3ef9c2fc154742a168df9329d89cfd4b8a020";
const closureSHA256 = "16cc2019f023a42a9efde1bfada1b53b5a64e96625a93d5db3affe36b13f72f0";
const hash = (data) => createHash("sha256").update(data).digest("hex");
const label = process.argv[2];
assert.match(label ?? "", /^[a-z0-9]+(?:-[a-z0-9]+)*$/, "Fresh bounded build label required");
const cache = join(root, ".cache", "ado-collection", "published-v11", label);
const artifacts = join(root, ".artifacts", "ado-collection");
const work = join(root, ".cache", "ado-collection", "work");
assert.ok(!existsSync(cache), "Refusing to replace a pinned build");
const git = (...args) => execFileSync("git", ["--no-pager", ...args], { cwd: root, maxBuffer: 4 << 20 });
const entries = git("ls-tree", "-r", "--format=%(objectname)%x09%(path)", origin).toString().trim().split(/\r?\n/)
  .map((line) => { const [gitBlob, path] = line.split("\t"); return { gitBlob, path }; })
  .filter(({ path }) => path === "go.mod" || path === "go.sum" ||
    /^internal\/(app|parsers|providers|connectors|evidence)\/.*\.go$/.test(path) && !path.endsWith("_test.go"))
  .map(({ path, gitBlob }) => ({ path: path.replaceAll("/", "\\"), gitBlob }))
  .sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
assert.equal(entries.length, 89, "Published production closure changed");
assert.equal(hash(entries.map(({ path, gitBlob }) => `${path}\0${gitBlob}\n`).join("")), closureSHA256,
  "Published path/blob closure differs from authored identity");
for (const path of [cache, artifacts, work]) mkdirSync(path, { recursive: true });
const files = entries.map(({ path, gitBlob }) => {
  const data = git("cat-file", "blob", gitBlob);
  const target = join(cache, ...path.split("\\"));
  mkdirSync(join(target, ".."), { recursive: true });
  writeFileSync(target, data, { flag: "wx" });
  return { path, gitBlob, sha256: hash(data), bytes: data.length };
});
assert.ok(files.reduce((sum, value) => sum + value.bytes, 0) < (2 << 20), "Bounded closure exceeded 2 MiB");
const helper = readFileSync(join(root, "tests", "jira_work_items", "ado", "published-core.main.go.txt"));
const command = join(cache, "cmd", "owned-v11-core");
mkdirSync(command, { recursive: true });
writeFileSync(join(command, "main.go"), helper, { flag: "wx" });
const executable = join(cache, "owned-v11-core.exe");
const go = join(root, ".cache", "modules", "golang.org", "toolchain@v0.0.1-go1.27.1.windows-amd64", "bin", "go.exe");
assert.ok(existsSync(go), "Cached Go 1.27.1 required; no automatic install/restore");
const env = { ...process.env };
for (const name of Object.keys(env)) {
  if (/^(ASPM_|ASMP_|ADO_|AZDO_|VSS_|TEAMS_|JIRA_|SLACK_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_)|^(SYSTEM_ACCESSTOKEN|HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$/i.test(name)) delete env[name];
}
Object.assign(env, {
  GOTOOLCHAIN: "local", GOENV: "off", GOWORK: "off", GOFLAGS: "", GOPROXY: "off", GOSUMDB: "off",
  GOMODCACHE: join(root, ".cache", "modules"), GOCACHE: join(root, ".cache", "build"),
  GOPATH: join(root, ".cache", "ado-collection", "gopath"), GOTMPDIR: work, TEMP: work, TMP: work, TMPDIR: work,
});
const args = ["build", "-mod=readonly", "-o", executable, ".\\cmd\\owned-v11-core"];
const result = spawnSync(go, args, { cwd: cache, env, encoding: "utf8", timeout: 120_000, maxBuffer: 4 << 20 });
writeFileSync(join(artifacts, `${label}.published-v11-build.log`), `${result.stdout ?? ""}${result.stderr ?? ""}`, { flag: "wx" });
if (result.error || result.status !== 0) {
  console.error("Published V11 build failed; bounded log retained, no dependency install.");
  process.exit(result.status ?? 1);
}
const receipt = {
  origin, closureSHA256, files, executable, executableSHA256: hash(readFileSync(executable)),
  helperSHA256: hash(helper), arguments: args, exitCode: 0,
  businessSQLSeeds: false, providerCalls: false, dataExecution: false,
};
writeFileSync(join(artifacts, `${label}.published-v11-build.json`), JSON.stringify(receipt, null, 2) + "\n", { flag: "wx" });
console.log("Exact published V11 closure built with the forwarding helper. No fixture/data execution.");
