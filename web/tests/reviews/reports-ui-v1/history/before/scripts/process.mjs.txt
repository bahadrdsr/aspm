import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export const root = fileURLToPath(new URL("..", import.meta.url));
export const web = join(root, "web");
export const cache = join(root, ".cache");
for (const name of ["gopath", "modules", "build", "work"]) mkdirSync(join(cache, name), { recursive: true });
export const environment = {
  ...process.env, GOTOOLCHAIN: "go1.27.1", GOWORK: "off",
  GOPATH: join(cache, "gopath"), GOMODCACHE: join(cache, "modules"),
  GOCACHE: join(cache, "build"), GOTMPDIR: join(cache, "work"),
  TEMP: join(cache, "work"), TMP: join(cache, "work"), TMPDIR: join(cache, "work"),
  NODE_USE_SYSTEM_CA: "1",
};
if (environment.NODE_TLS_REJECT_UNAUTHORIZED === "0") throw new Error("TLS verification must remain enabled.");

export function run(command, args, cwd = root) {
  const result = spawnSync(command, args, { cwd, env: environment, stdio: "inherit" });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} failed with exit ${result.status ?? "unknown"}.`);
}

export function npm(script) {
  if (!/^[a-z][a-z:-]*$/.test(script)) throw new Error("Unknown npm script name.");
  if (process.platform === "win32") run(process.env.ComSpec ?? "cmd.exe", ["/d", "/s", "/c", `npm.cmd run ${script}`], web);
  else run("npm", ["run", script], web);
}
