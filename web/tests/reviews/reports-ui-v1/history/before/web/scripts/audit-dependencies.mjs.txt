import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));
const lock = JSON.parse(readFileSync(join(root, "package-lock.json"), "utf8"));
const manifest = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
for (const group of ["dependencies", "devDependencies"]) {
  assert.deepEqual(manifest[group], lock.packages[""][group], `Lockfile ${group} differs from the manifest.`);
  for (const version of Object.values(manifest[group])) assert.match(version, /^\d+\.\d+\.\d+$/, "Direct dependencies must be stable exact versions.");
}
const allowed = new Set(["MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "0BSD", "(MIT OR Apache-2.0)", "MPL-2.0"]);
const packages = [];
const notices = [
  "aspm M01 runtime dependency notices",
  "Generated from the installed, integrity-locked package graph. Package metadata is not independent legal or release approval.",
];
for (const [location, entry] of Object.entries(lock.packages).sort(([a], [b]) => a.localeCompare(b))) {
  if (!location) continue;
  const name = location.split("node_modules/").at(-1);
  const directory = join(root, ...location.split("/"));
  const installed = existsSync(join(directory, "package.json"));
  const pkg = installed ? JSON.parse(readFileSync(join(directory, "package.json"), "utf8")) : null;
  if (pkg) assert.equal(pkg.version, entry.version, `Installed version mismatch: ${name}`);
  const license = entry.license ?? pkg?.license;
  assert.ok(allowed.has(license), `Unreviewed license expression: ${name}@${entry.version}: ${license}`);
  assert.ok(entry.integrity, `Missing archive integrity for ${name}`);
  const record = { name, version: entry.version, license, developmentOnly: entry.dev === true, optional: entry.optional === true, installed, archiveIntegrity: entry.integrity, noticeSHA256: [] };
  if (!entry.dev && installed) {
    const files = readdirSync(directory).filter((file) => /^(licen[sc]e|copying|notice)(?:\.|$)/i.test(file));
    if (name === "@radix-ui/react-compose-refs") {
      const text = readFileSync(join(root, "public", "notices", "radix-MIT.txt"), "utf8");
      notices.push(`\n${name}@${entry.version} (${license})\nUpstream Radix notice retained for the package that omits its license file.\n${text}`);
      record.noticeSHA256.push(createHash("sha256").update(text).digest("hex"));
    } else {
      assert.ok(files.length > 0, `Missing installed runtime notice: ${name}`);
      for (const file of files) {
        const text = readFileSync(join(directory, file), "utf8");
        assert.ok(!/Commons Clause/i.test(text), `Restricted runtime notice requires review: ${name}`);
        notices.push(`\n${name}@${entry.version} (${license}) / ${file}\n${text}`);
        record.noticeSHA256.push(createHash("sha256").update(text).digest("hex"));
      }
    }
  }
  packages.push(record);
}
const directory = join(root, "public", "notices");
mkdirSync(directory, { recursive: true });
writeFileSync(join(directory, "dependencies.txt"), `${notices.join("\n")}\n`);
const report = {
  schemaVersion: 1, scope: "M01 browser build and runtime lock graph",
  approval: "pending-independent-distribution-review",
  limitation: "Some mirror metadata supplies SHA-1 archive integrity. Installed notices additionally have SHA-256 digests; this does not independently authenticate registry origin or complete every platform's file audit.",
  packages,
};
writeFileSync(join(directory, "dependency-inventory.json"), `${JSON.stringify(report, null, 2)}\n`);
const sbom = {
  bomFormat: "CycloneDX", specVersion: "1.6", version: 1,
  metadata: { component: { type: "application", name: manifest.name, version: manifest.version, licenses: [{ license: { id: manifest.license } }] } },
  components: packages.map((p) => ({ type: "library", "bom-ref": `pkg:npm/${p.name}@${p.version}`, name: p.name, version: p.version, purl: `pkg:npm/${p.name.replace("@", "%40")}@${p.version}`, licenses: [{ expression: p.license }], scope: p.developmentOnly ? "excluded" : p.optional ? "optional" : "required", properties: [{ name: "aspm:archive-integrity", value: p.archiveIntegrity }, { name: "aspm:installed-on-build-host", value: String(p.installed) }] })),
};
writeFileSync(join(directory, "sbom.cdx.json"), `${JSON.stringify(sbom, null, 2)}\n`);
console.log(`Dependency inventory: ${packages.length} locked packages, ${packages.filter((p) => !p.developmentOnly && p.installed).length} installed runtime-graph notice sets. SBOM and notices generated; distribution review remains separate.`);
