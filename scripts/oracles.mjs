import { spawnSync } from "node:child_process";
import { isAbsolute, relative, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import {
  authorityFingerprint, captureOracle, gitSourceFiles, oracleEnvironment, oracleFileDigest,
  oraclePath, publishOracle, readOracleJSON, treeDigest, verifyOracle, withOracleGuard,
} from "./oracle-lib.mjs";

const help = `Protected oracle commands (no update/key-generation mode):
  verify --root ROOT --manifest FILE --signature FILE --trusted-key PUBLIC.pem
         [--trusted-fingerprint sha256:SPKI_DIGEST]
  capture|seal --root ROOT --manifest NEW_FILE --signature NEW_FILE
         --signing-key PRIVATE.pem --scope SCOPE --author AUTHOR
         --phase pre-code-red|reviewed-baseline --files FILE_LIST.json
         --protected-roots ROOT_LIST.json --bindings BINDING_LIST.json
         [--red-log FILE --red-exit-code NONZERO]
  digest --root ROOT (--files FILE_LIST.json | --git)
  fingerprint --trusted-key PUBLIC.pem
  check --root ROOT --config CONFIG.json --config-digest sha256:DIGEST
         --trusted-key PUBLIC.pem --trusted-fingerprint sha256:DIGEST
         (--files FILE_LIST.json | --git) -- COMMAND ARG...
  check-env [--root ROOT] -- COMMAND ARG...

capture/seal only create new files. pre-code-red requires a genuine prior RED
log and nonzero exit code. Recovered existing tests are reviewed-baseline.
File lists contain portable relative paths or capture {path,normalization}
entries; normalization is none (default at capture) or explicitly signed lf.
check-env requires ASPM_ORACLE_CONFIG, ASPM_ORACLE_CONFIG_SHA256,
ASPM_ORACLE_TRUSTED_KEY_FILE and ASPM_ORACLE_TRUSTED_FINGERPRINT from the caller.
CI must invoke a checker from an independently protected revision.
`;

const definitions = Object.fromEntries([
  "root", "manifest", "signature", "trusted-key", "trusted-fingerprint",
  "signing-key", "scope", "author", "phase", "files", "protected-roots", "bindings",
  "red-log", "red-exit-code", "config", "config-digest",
].map((name) => [name, { type: "string" }]));
definitions.git = { type: "boolean" };
definitions.help = { type: "boolean" };

function required(flags, name) {
  if (typeof flags[name] !== "string" || flags[name].trim() === "") throw new Error(`Oracle: explicit --${name} is required`);
  return flags[name];
}

function exactFlags(flags, allowed) {
  const accepted = new Set(["help", ...allowed]);
  for (const name of Object.keys(flags)) {
    if (!accepted.has(name)) throw new Error(`Oracle: --${name} is not supported for this command`);
  }
}

function outputPath(root, value) {
  const path = relative(resolve(root), resolve(root, value));
  if (isAbsolute(path) || path === ".." || path.startsWith(`..${sep}`)) throw new Error("Oracle: output path must remain inside the source root");
  return oraclePath(path.split(sep).join("/"));
}

function selectedFiles(flags, root) {
  if (Boolean(flags.git) === Boolean(flags.files)) throw new Error("Oracle: select exactly one explicit --files scope or --git source scope");
  if (flags.git) return () => gitSourceFiles(root);
  return () => {
    const paths = readOracleJSON(required(flags, "files"));
    if (!Array.isArray(paths) || paths.some((path) => typeof path !== "string")) {
      throw new Error("Oracle: digest/check file scope must be an explicit array of paths");
    }
    return paths;
  };
}

export function main(argv = process.argv.slice(2)) {
  const [command, ...args] = argv;
  if (!command || command === "help" || command === "--help") {
    console.log(help);
    if (!command) throw new Error("Oracle: choose an explicit command");
    return;
  }
  const separator = args.indexOf("--");
  const options = separator < 0 ? args : args.slice(0, separator);
  const child = separator < 0 ? [] : args.slice(separator + 1);
  const { values: flags } = parseArgs({ args: options, options: definitions, strict: true, allowPositionals: false });
  if (flags.help) { console.log(help); return; }
  if (child.length && !["check", "check-env"].includes(command)) throw new Error("Oracle: only check commands can run a supplied test command");
  if (command === "fingerprint") {
    exactFlags(flags, ["trusted-key"]);
    console.log(authorityFingerprint(required(flags, "trusted-key")));
    return;
  }
  const root = resolve(command === "check-env" ? flags.root ?? process.cwd() : required(flags, "root"));
  if (command === "verify") {
    exactFlags(flags, ["root", "manifest", "signature", "trusted-key", "trusted-fingerprint"]);
    console.log(JSON.stringify(verifyOracle({
      root, manifestPath: resolve(root, required(flags, "manifest")),
      signaturePath: resolve(root, required(flags, "signature")),
      trustedKeyPath: required(flags, "trusted-key"), trustedKeyFingerprint: flags["trusted-fingerprint"],
    }), null, 2));
    return;
  }
  if (command === "capture" || command === "seal") {
    exactFlags(flags, ["root", "manifest", "signature", "signing-key", "scope", "author", "phase", "files", "protected-roots", "bindings", "red-log", "red-exit-code"]);
    const phase = required(flags, "phase");
    let redEvidence = null;
    if (flags["red-log"] || flags["red-exit-code"] || phase === "pre-code-red") {
      const exit = required(flags, "red-exit-code");
      if (!/^[1-9][0-9]{0,2}$/.test(exit) || Number(exit) > 255) throw new Error("Oracle: a genuine nonzero RED exit code (1-255) is required");
      redEvidence = { sha256: oracleFileDigest(required(flags, "red-log")).slice(7), exitCode: Number(exit) };
    }
    const captured = captureOracle({
      root, phase, scope: required(flags, "scope"), author: required(flags, "author"), redEvidence,
      signingKeyPath: required(flags, "signing-key"),
      files: readOracleJSON(required(flags, "files")),
      protectedRoots: readOracleJSON(required(flags, "protected-roots")),
      bindingPaths: readOracleJSON(required(flags, "bindings")),
    });
    const manifest = outputPath(root, required(flags, "manifest"));
    const signature = outputPath(root, required(flags, "signature"));
    publishOracle(root, manifest, signature, captured);
    console.log(JSON.stringify({
      event: "new-test-author-capture", scope: captured.manifest.scope, phase,
      author: captured.manifest.author, capturedAt: captured.manifest.capturedAt,
      redEvidence, manifest, signature, manifestDigest: oracleFileDigest(resolve(root, manifest)),
    }, null, 2));
    return;
  }
  if (command === "digest") {
    exactFlags(flags, ["root", "files", "git"]);
    const files = selectedFiles(flags, root)();
    console.log(JSON.stringify({ treeDigest: treeDigest({ root, files }), fileCount: files.length }));
    return;
  }
  if (command === "check" || command === "check-env") {
    if (!child.length) throw new Error("Oracle: check requires an explicit command after --");
    let guard;
    if (command === "check-env") {
      exactFlags(flags, ["root"]);
      guard = oracleEnvironment(root);
    } else {
      exactFlags(flags, ["root", "config", "config-digest", "trusted-key", "trusted-fingerprint", "files", "git"]);
      guard = {
        root, configPath: required(flags, "config"), configDigest: required(flags, "config-digest"),
        trustedKeyPath: required(flags, "trusted-key"), trustedKeyFingerprint: required(flags, "trusted-fingerprint"),
        files: selectedFiles(flags, root),
      };
    }
    withOracleGuard(guard, () => {
      const result = spawnSync(child[0], child.slice(1), { cwd: root, env: process.env, stdio: "inherit", windowsHide: true, shell: false });
      if (result.error || result.status !== 0) throw new Error(`Oracle: guarded command failed (exit ${result.status ?? "unavailable"}); no successful acceptance`);
    });
    return;
  }
  throw new Error("Oracle: unknown command; no baseline or key was changed");
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try { main(); } catch (error) {
    console.error(error.message);
    if (error instanceof AggregateError) for (const cause of error.errors) console.error(cause.message);
    process.exitCode = 1;
  }
}
