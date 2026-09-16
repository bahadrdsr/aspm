import { mkdirSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const work = join(root, ".cache", "work");
const browsers = join(root, ".cache", "playwright");
mkdirSync(work, { recursive: true });
mkdirSync(browsers, { recursive: true });
const require = createRequire(import.meta.url);
const cli = require.resolve("@playwright/test/cli");
if (process.env.NODE_TLS_REJECT_UNAUTHORIZED === "0") {
  throw new Error("TLS certificate verification must remain enabled.");
}
const result = spawnSync(process.execPath, [cli, ...process.argv.slice(2)], {
  cwd: root,
  stdio: "inherit",
  env: {
    ...process.env,
    NODE_USE_SYSTEM_CA: "1",
    PLAYWRIGHT_BROWSERS_PATH: browsers,
    TEMP: work,
    TMP: work,
    TMPDIR: work,
  },
});
if (result.error) {
  console.error(result.error.message);
}
process.exit(result.status ?? 1);
