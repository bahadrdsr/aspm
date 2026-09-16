import { createHash } from "node:crypto";
import assert from "node:assert/strict";
import { cpSync, existsSync, lstatSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { join, relative, sep } from "node:path";
import { environment, npm, root, run } from "./process.mjs";

function capture(command, args) {
  const result = spawnSync(command, args, { cwd: root, env: environment, encoding: "utf8" });
  if (result.error) throw result.error;
  assert.equal(result.status, 0, `Unable to record ${command} build metadata.`);
  return result.stdout.trim();
}

npm("check:lock");
npm("build");
const directory = join(root, ".artifacts", "development");
if (existsSync(directory)) {
  assert.ok(lstatSync(directory).isDirectory() && !lstatSync(directory).isSymbolicLink(), "Refusing to replace a non-directory build output.");
  rmSync(directory, { recursive: true });
}
mkdirSync(directory, { recursive: true });
const binary = `aspm-dev${process.platform === "win32" ? ".exe" : ""}`;
run("go", ["build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-o", join(directory, binary), `.${sep}${join("cmd", "aspm-dev")}`]);
cpSync(join(root, "web", "dist"), join(directory, "web"), { recursive: true });
cpSync(join(root, "LICENSE"), join(directory, "LICENSE"));
cpSync(join(root, "NOTICE"), join(directory, "NOTICE"));
cpSync(join(capture("go", ["env", "GOROOT"]), "LICENSE"), join(directory, "Go-LICENSE.txt"));
function files(path) {
  return readdirSync(path, { withFileTypes: true }).flatMap((entry) => entry.isDirectory() ? files(join(path, entry.name)) : [join(path, entry.name)]);
}
const entries = files(directory).filter((path) => !path.endsWith("artifact-manifest.json")).sort().map((path) => ({
  path: relative(directory, path).replaceAll("\\", "/"),
  sha256: createHash("sha256").update(readFileSync(path)).digest("hex"),
}));
writeFileSync(join(directory, "artifact-manifest.json"), `${JSON.stringify({
  schemaVersion: 1, kind: "DevelopmentArtifactManifest", application: "aspm", milestone: "M01",
  productionReady: false, platform: process.platform, architecture: process.arch,
  sourceRevision: capture("git", ["rev-parse", "HEAD"]),
  sourceDirty: capture("git", ["status", "--porcelain"]) !== "",
  goVersion: capture("go", ["version"]),
  lockSHA256: createHash("sha256").update(readFileSync(join(root, "web", "package-lock.json"))).digest("hex"),
  dataAPIs: "unavailable", files: entries,
}, null, 2)}\n`);
console.log(`Development output: ${directory}. Not a supported release or a published artifact.`);
