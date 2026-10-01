import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../", import.meta.url));
const origin = "4a54db75046f5910705007d800fa34496adc22d8";
const label = process.argv[2];
assert.match(label ?? "", /^[a-z0-9]+(?:-[a-z0-9]+)*$/, "Fresh bounded build label required");
const output = join(root, ".artifacts", "jira-work-items");
const cache = join(root, ".cache", "jira-work-items", "published-v9", label);
const work = join(root, ".cache", "jira-work-items", "work");
assert.ok(!existsSync(cache), "Refusing to replace a pinned fixture build");
for (const path of [output, cache, work]) mkdirSync(path, { recursive: true });
const hash = (data) => createHash("sha256").update(data).digest("hex");
const git = (...args) => execFileSync("git", ["--no-pager", ...args], { cwd: root, maxBuffer: 4 << 20 });
const trees = ["app", "parsers", "providers", "connectors", "evidence"].map((name) => `internal/${name}`);
const paths = ["go.mod", "go.sum", ...git("ls-tree", "-r", "--name-only", origin, "--", ...trees)
  .toString().trim().split(/\r?\n/).filter((path) => path.endsWith(".go") && !path.endsWith("_test.go"))].sort();
assert.ok(paths.length <= 120, "Pinned production dependency closure exceeded its file bound");
const files = [];
for (const path of paths) {
  const bytes = git("show", `${origin}:${path}`);
  const target = join(cache, ...path.split("/"));
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, bytes, { flag: "wx" });
  files.push({ path: path.replaceAll("/", "\\"), bytes: bytes.length, sha256: hash(bytes),
    gitBlob: git("rev-parse", `${origin}:${path}`).toString().trim() });
}
assert.ok(files.reduce((sum, file) => sum + file.bytes, 0) < (2 << 20), "Pinned source size bound exceeded");
const helper = join(root, "tests", "jira_work_items", "testdata", "published-core.main.go.txt");
const main = join(cache, "cmd", "owned-v9-core", "main.go");
mkdirSync(dirname(main), { recursive: true });
writeFileSync(main, readFileSync(helper), { flag: "wx" });
const executable = join(cache, "owned-v9-core.exe");
const go = join(root, ".cache", "modules", "golang.org", "toolchain@v0.0.1-go1.27.1.windows-amd64", "bin", "go.exe");
assert.ok(existsSync(go), "Cached Go1.27.1 required; no install or restore authorized");
const env = { ...process.env };
for (const name of Object.keys(env)) {
  if (/^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_|JIRA_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$/i.test(name)) delete env[name];
}
Object.assign(env, {
  GOTOOLCHAIN: "local", GOENV: "off", GOWORK: "off", GOFLAGS: "", GOPROXY: "off", GOSUMDB: "off",
  GOMODCACHE: join(root, ".cache", "modules"), GOCACHE: join(root, ".cache", "build"),
  GOPATH: join(root, ".cache", "jira-work-items", "gopath"), GOTMPDIR: work, TEMP: work, TMP: work, TMPDIR: work,
});
const args = ["build", "-mod=readonly", "-o", executable, ".\\cmd\\owned-v9-core"];
const result = spawnSync(go, args, { cwd: cache, env, encoding: "utf8", timeout: 120_000, maxBuffer: 4 << 20 });
writeFileSync(join(output, `${label}.published-v9-build.log`), `${result.stdout ?? ""}${result.stderr ?? ""}`, { flag: "wx" });
if (result.error || result.status !== 0) {
  console.error("Exact published V9 fixture build failed; see bounded build log. No dependencies installed.");
  process.exit(result.status ?? 1);
}
const receipt = {
  origin, executable, executableSHA256: hash(readFileSync(executable)), helperSHA256: hash(readFileSync(helper)),
  arguments: args, files, businessSQLSeeds: false, providerCalls: false, dataExecution: false, exitCode: 0,
};
writeFileSync(join(output, `${label}.published-v9-build.json`), JSON.stringify(receipt, null, 2) + "\n", { flag: "wx" });
console.log(`Pinned V9 app compiled from ${files.length} production files plus a real-HTTP/control helper; no business SQL or data execution.`);
