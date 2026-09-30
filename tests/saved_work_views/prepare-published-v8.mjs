import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../", import.meta.url));
const origin = "629bebd727b448fb89eb732485457fd60cbffbfd";
const label = process.argv[2];
assert.match(label ?? "", /^[a-z0-9]+(?:-[a-z0-9]+)*$/, "Fresh bounded build label required");
const output = join(root, ".artifacts", "saved-work-views-v1");
const cache = join(root, ".cache", "saved-work-views-v1", "published-v8", label);
const work = join(root, ".cache", "saved-work-views-v1", "work");
assert.ok(!existsSync(cache), "Refusing to overwrite a published fixture build");
for (const path of [output, cache, work]) mkdirSync(path, { recursive: true });
const hash = (data) => createHash("sha256").update(data).digest("hex");
const git = (...args) => execFileSync("git", ["--no-pager", ...args], { cwd: root, maxBuffer: 4 << 20 });
const trees = ["app", "parsers", "providers", "connectors", "evidence"].map((name) => `internal/${name}`);
const paths = ["go.mod", "go.sum", ...git("ls-tree", "-r", "--name-only", origin, "--", ...trees)
  .toString().trim().split(/\r?\n/).filter((path) => path.endsWith(".go") && !path.endsWith("_test.go"))].sort();
const files = [];
for (const path of paths) {
  const bytes = git("show", `${origin}:${path}`);
  const target = join(cache, ...path.split("/"));
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, bytes, { flag: "wx" });
  files.push({ path: path.replaceAll("/", "\\"), bytes: bytes.length, sha256: hash(bytes),
    gitBlob: git("rev-parse", `${origin}:${path}`).toString().trim() });
}
const helper = join(root, "tests", "saved_work_views", "testdata", "seed-v8.main.go.txt");
const main = join(cache, "cmd", "owned-v8-seed", "main.go");
mkdirSync(dirname(main), { recursive: true });
writeFileSync(main, readFileSync(helper), { flag: "wx" });
const executable = join(cache, "owned-v8-seed.exe");
const go = join(root, ".cache", "modules", "golang.org", "toolchain@v0.0.1-go1.27.1.windows-amd64", "bin", "go.exe");
assert.ok(existsSync(go), "Offline cached Go 1.27.1 required; no install");
const env = { ...process.env };
for (const name of Object.keys(env)) {
  if (/^(ASPM_|AWS_|AZURE_|OPENAI_|ANTHROPIC_|GITHUB_|GH_|SLACK_)|^(HTTP_PROXY|HTTPS_PROXY|ALL_PROXY)$/i.test(name)) delete env[name];
}
Object.assign(env, {
  GOTOOLCHAIN: "local", GOENV: "off", GOWORK: "off", GOFLAGS: "", GOPROXY: "off", GOSUMDB: "off",
  GOMODCACHE: join(root, ".cache", "modules"), GOCACHE: join(root, ".cache", "build"),
  GOPATH: join(root, ".cache", "saved-work-views-v1", "gopath"), GOTMPDIR: work, TEMP: work, TMP: work, TMPDIR: work,
});
const args = ["build", "-mod=readonly", "-o", executable, ".\\cmd\\owned-v8-seed"];
const result = spawnSync(go, args, { cwd: cache, env, encoding: "utf8", timeout: 120_000, maxBuffer: 4 << 20 });
writeFileSync(join(output, `${label}.published-v8-build.log`), `${result.stdout ?? ""}${result.stderr ?? ""}`, { flag: "wx" });
if (result.error || result.status !== 0) {
  console.error("Exact published V8 fixture build failed; see bounded build log. No install attempted.");
  process.exit(result.status ?? 1);
}
const receipt = {
  origin, executable, executableSHA256: hash(readFileSync(executable)),
  helperSHA256: hash(readFileSync(helper)), arguments: args, files,
  businessSQLSeeds: false, providerCalls: false, dataExecution: false, exitCode: 0,
};
writeFileSync(join(output, `${label}.published-v8-build.json`), JSON.stringify(receipt, null, 2) + "\n", { flag: "wx" });
console.log(`Exact published V8 app fixture compiled from ${files.length} pinned inputs plus one HTTP-only seed helper; not data execution.`);
