import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

if (process.argv.length !== 2) throw new Error("This frozen runner takes no arguments or update mode.");
const packet = dirname(fileURLToPath(import.meta.url));
const root = resolve(packet, "..", "..");
const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) =>
  /^(PATH|PATHEXT|SYSTEMROOT|WINDIR|COMSPEC)$/i.test(key)));
const result = spawnSync(process.execPath, [
  "--test", "--test-concurrency=1", "--test-reporter=tap", join(packet, "publication.test.mjs"),
], { cwd: root, env: environment, stdio: "inherit", timeout: 180_000, windowsHide: true });
if (result.error) throw result.error;
if (result.signal) throw new Error(`Publication tests terminated by ${result.signal}.`);
process.exitCode = result.status ?? 1;
