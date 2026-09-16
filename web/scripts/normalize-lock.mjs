import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const path = fileURLToPath(new URL("../package-lock.json", import.meta.url));
const lock = JSON.parse(readFileSync(path, "utf8"));
assert.equal(lock.lockfileVersion, 3);
let changed = 0;
for (const [location, entry] of Object.entries(lock.packages)) {
  if (!location) continue;
  assert.match(entry.version, /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/);
  assert.match(entry.integrity, /^sha(?:1|256|384|512)-[A-Za-z0-9+/=]+$/);
  const name = location.split("node_modules/").at(-1);
  const canonical = `https://registry.npmjs.org/${name}/-/${name.split("/").at(-1)}-${entry.version}.tgz`;
  if (entry.resolved === canonical) continue;
  const origin = new URL(entry.resolved);
  assert.ok(origin.protocol === "https:" && (origin.hostname === "packagefeedproxy.microsoft.io" || /^ms-feed-\d+\.pkgs\.visualstudio\.com$/.test(origin.hostname)), `Unreviewed archive origin for ${name}`);
  assert.ok(decodeURIComponent(origin.pathname).endsWith(`/${name}/-/${name.split("/").at(-1)}-${entry.version}.tgz`), `Archive identity mismatch for ${name}`);
  entry.resolved = canonical;
  changed += 1;
}
if (process.argv.includes("--check")) {
  assert.equal(changed, 0, "Run node scripts/normalize-lock.mjs after a mirror restore; do not change package versions or integrity.");
} else if (changed) {
  writeFileSync(path, `${JSON.stringify(lock, null, 2)}\n`);
}
console.log(`Lockfile: ${Object.keys(lock.packages).length - 1} exact archive records; ${changed} mirror URLs normalized, versions/integrities preserved.`);
