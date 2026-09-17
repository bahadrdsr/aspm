import { spawnSync } from "node:child_process";
import { cpSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const output = join(root, ".artifacts", "container");
const architecture = process.env.ASPM_IMAGE_ARCH ?? "amd64";
if (!["amd64", "arm64"].includes(architecture)) throw new Error("Unsupported image architecture.");
const commands = ["core-api", "ingestion", "report-worker", "delivery-worker", "collection-worker", "aspmctl"];

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: root, stdio: "inherit", ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} failed with exit ${result.status}`);
}

run(process.execPath, [join(root, "web", "node_modules", "typescript", "bin", "tsc"), "--noEmit"], { cwd: join(root, "web") });
run(process.execPath, [join(root, "web", "scripts", "audit-dependencies.mjs")]);
run(process.execPath, [join(root, "web", "node_modules", "vite", "bin", "vite.js"), "build"], { cwd: join(root, "web") });
mkdirSync(join(output, "bin"), { recursive: true });
for (const command of commands) {
  run("go", ["build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-o", join(output, "bin", command), `./cmd/${command}`], {
    env: { ...process.env, GOOS: "linux", GOARCH: architecture, CGO_ENABLED: "0" },
  });
}
cpSync(join(root, "web", "dist"), join(output, "web"), { recursive: true });
for (const file of ["LICENSE", "NOTICE"]) cpSync(join(root, file), join(output, file));
cpSync(join(root, "deploy", "Containerfile.runtime"), join(output, "Containerfile"));
const files = {};
function inventory(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) inventory(path);
    else if (entry.name !== "manifest.json") files[relative(output, path).replaceAll("\\", "/")] = createHash("sha256").update(readFileSync(path)).digest("hex");
  }
}
inventory(output);
writeFileSync(join(output, "manifest.json"), JSON.stringify({
  format: "aspm/container-artifacts/v1", platform: `linux/${architecture}`, buildMethod: "host-compiled-static-artifacts",
  scope: "Not a release signature or a fully validated source-container build", files,
}, null, 2) + "\n");
console.log(`Container build context prepared: ${output}`);
