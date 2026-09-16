import { createHash, createPrivateKey, createPublicKey, sign, verify } from "node:crypto";
import {
  closeSync, constants, existsSync, fstatSync, fsyncSync, lstatSync, mkdirSync,
  openSync, readSync, readdirSync, realpathSync, unlinkSync, writeFileSync,
} from "node:fs";
import { isAbsolute, join, relative, resolve, sep } from "node:path";
import { spawnSync } from "node:child_process";

const maximumFileBytes = 64 * 1024 * 1024;
const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
const fail = (message) => { throw new Error(`Oracle: ${message}`); };
const text = (value) => typeof value === "string" && value.length > 0 &&
  value.length <= 1024 && value.trim() === value && !/[\x00-\x1f\x7f]/.test(value);

export function oraclePath(value) {
  if (!text(value) || isAbsolute(value) || /[\\<>:"|?*]/.test(value)) fail("path must be a portable relative file path inside the source root");
  const parts = value.split("/");
  if (parts.some((part) => !part || part === "." || part === ".." || /[. ]$/.test(part) ||
    /^(con|prn|aux|nul|com[0-9]|lpt[0-9])(?:\.|$)/i.test(part))) fail("path escapes or aliases the source root");
  return value;
}

function pathList(values, label, allowEmpty = false) {
  if (!Array.isArray(values) || values.length > 100000 || (!allowEmpty && values.length === 0)) fail(`${label} requires an explicit file/path list`);
  const seen = new Set();
  return values.map((value) => {
    oraclePath(value);
    const identity = value.toLowerCase();
    if (seen.has(identity)) fail(`${label} contains duplicate or case-aliased paths`);
    seen.add(identity);
    return value;
  });
}

function sourceRoot(root) {
  if (!text(root)) fail("source root must be explicitly supplied");
  const path = resolve(root);
  const stat = lstatSync(path);
  if (!stat.isDirectory() || stat.isSymbolicLink()) fail("source root must be a real directory, not a symlink");
  return realpathSync(path);
}

function sourcePath(root, name, optional = false) {
  oraclePath(name);
  let path = root;
  for (const segment of name.split("/")) {
    path = join(path, segment);
    let stat;
    try { stat = lstatSync(path); } catch (error) {
      if (optional && error.code === "ENOENT") return null;
      fail(`missing or inaccessible oracle path: ${name}`);
    }
    if (stat.isSymbolicLink()) fail(`symlink path is forbidden: ${name}`);
    const inside = relative(root, realpathSync(path));
    if (isAbsolute(inside) || inside === ".." || inside.startsWith(`..${sep}`)) fail(`path outside source root: ${name}`);
  }
  return path;
}

function readRegular(path, limit = maximumFileBytes) {
  let fd;
  try {
    const before = lstatSync(path);
    if (!before.isFile() || before.isSymbolicLink() || before.nlink > 1 || before.size > limit) fail("input must be a bounded regular file without links");
    fd = openSync(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0));
    const opened = fstatSync(fd);
    if (opened.dev !== before.dev || opened.ino !== before.ino || !opened.isFile()) fail("input changed while opening");
    const buffer = Buffer.alloc(opened.size + 1);
    let size = 0;
    while (size < buffer.length) {
      const count = readSync(fd, buffer, size, buffer.length - size, null);
      if (count === 0) break;
      size += count;
    }
    const data = buffer.subarray(0, size);
    const after = fstatSync(fd);
    const named = lstatSync(path);
    if (data.length > limit || data.length !== opened.size || after.size !== opened.size ||
      after.mtimeMs !== opened.mtimeMs || after.ctimeMs !== opened.ctimeMs ||
      named.isSymbolicLink() || named.dev !== after.dev || named.ino !== after.ino) fail("input changed while reading");
    return data;
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}

export function readOracleFile(root, name) {
  const base = sourceRoot(root);
  return readRegular(sourcePath(base, name));
}

function normalized(bytes, mode) {
  if (mode === "none") return bytes;
  if (mode !== "lf") fail("normalization must be explicitly signed as none or lf");
  const output = Buffer.allocUnsafe(bytes.length);
  let size = 0;
  for (let i = 0; i < bytes.length; i++) {
    if (bytes[i] === 13 && bytes[i + 1] === 10) continue;
    output[size++] = bytes[i];
  }
  return output.subarray(0, size);
}

function publicAuthority(path, fingerprint) {
  if (!text(path)) fail("an explicitly supplied trusted authority public-key file is required");
  let key;
  try {
    const pem = readRegular(resolve(path), 65536).toString("utf8");
    if (!/^\s*-----BEGIN PUBLIC KEY-----[\s\S]+-----END PUBLIC KEY-----\s*$/.test(pem)) fail("public key required");
    key = createPublicKey(pem);
    if (key.type !== "public" || key.asymmetricKeyType !== "ed25519") fail("Ed25519 authority required");
  } catch {
    fail("trusted authority public key is unavailable or is not an Ed25519 public key");
  }
  const actual = `sha256:${sha256(key.export({ type: "spki", format: "der" }))}`;
  if (fingerprint !== undefined && (!/^sha256:[a-f0-9]{64}$/.test(fingerprint) || fingerprint !== actual)) fail("trusted authority fingerprint mismatch");
  return { key, fingerprint: actual };
}

function manifestShape(manifest) {
  if (!manifest || manifest.schemaVersion !== 1 || !text(manifest.scope) || !text(manifest.author) ||
    !["pre-code-red", "reviewed-baseline"].includes(manifest.phase) ||
    typeof manifest.capturedAt !== "string" || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{3})?Z$/.test(manifest.capturedAt) ||
    !Number.isFinite(Date.parse(manifest.capturedAt))) fail("invalid signed oracle capture metadata");
  if (manifest.phase === "pre-code-red" && !manifest.redEvidence) fail("pre-code-red requires signed RED evidence");
  if (manifest.redEvidence != null && (!/^[a-f0-9]{64}$/.test(manifest.redEvidence.sha256) ||
    !Number.isInteger(manifest.redEvidence.exitCode) || manifest.redEvidence.exitCode < 1 || manifest.redEvidence.exitCode > 255)) {
    fail("RED evidence requires its exact digest and a nonzero exit code");
  }
  const roots = pathList(manifest.protectedRoots, "protectedRoots");
  const bindings = pathList(manifest.bindingPaths, "bindingPaths", true);
  if (!Array.isArray(manifest.files)) fail("oracle files require an explicit test-author file list");
  const files = pathList(manifest.files.map((entry) => entry?.path), "files");
  for (const entry of manifest.files) {
    if (!["none", "lf"].includes(entry.normalization) || !/^[a-f0-9]{64}$/.test(entry.sha256)) fail("invalid signed per-file normalization or digest");
  }
  for (const binding of bindings) {
    if (!binding.endsWith("_test.go") || !roots.some((root) => binding.startsWith(`${root}/`))) fail("binding path must be an explicit Go forwarding file under a protected root");
  }
  return { roots, bindings, files };
}

function checkManifestFiles(root, manifest) {
  const shape = manifestShape(manifest);
  const listed = new Set(shape.files);
  const bindings = new Set(shape.bindings);
  for (const entry of manifest.files) {
    const bytes = readRegular(sourcePath(root, entry.path));
    if (sha256(normalized(bytes, entry.normalization)) !== entry.sha256) fail(`changed oracle file digest: ${entry.path}`);
  }
  let visited = 0;
  const walk = (name, depth) => {
    if (++visited > 100000 || depth > 64) fail("protected root traversal limit reached");
    const path = sourcePath(root, name);
    const stat = lstatSync(path);
    if (stat.isDirectory()) {
      for (const child of readdirSync(path).sort()) walk(`${name}/${child}`, depth + 1);
    } else if (!stat.isFile() || !listed.has(name) && !bindings.has(name)) {
      fail(`unlisted or unprotected file under protected root: ${name}`);
    }
  };
  for (const name of shape.roots) {
    const path = sourcePath(root, name);
    if (!lstatSync(path).isDirectory()) fail(`protected root is not a directory: ${name}`);
    walk(name, 0);
  }
  let bindingCount = 0;
  for (const name of shape.bindings) {
    const path = sourcePath(root, name, true);
    if (path !== null) {
      let source;
      try { source = new TextDecoder("utf-8", { fatal: true }).decode(readRegular(path)); }
      catch { fail(`binding is not a regular UTF-8 source file: ${name}`); }
      checkBinding(source, name);
      bindingCount++;
    }
  }
  return { ...shape, bindingCount };
}

export function verifyOracle({ root, manifestPath, signaturePath, trustedKeyPath, trustedKeyFingerprint } = {}) {
  const authority = publicAuthority(trustedKeyPath, trustedKeyFingerprint);
  if (!text(manifestPath) || !text(signaturePath)) fail("explicit oracle manifest and detached signature paths are required");
  const bytes = readRegular(resolve(manifestPath), 4 * 1024 * 1024);
  const encoded = readRegular(resolve(signaturePath), 512).toString("ascii").trim();
  const signature = Buffer.from(encoded, "base64");
  if (signature.length !== 64 || signature.toString("base64") !== encoded || !verify(null, bytes, authority.key, signature)) {
    fail("oracle signature does not match the trusted authority");
  }
  // No manifest-declared path is inspected until its exact bytes authenticate.
  let manifest;
  try { manifest = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)); }
  catch { fail("signed oracle manifest is not valid UTF-8 JSON"); }
  const shape = checkManifestFiles(sourceRoot(root), manifest);
  return {
    scope: manifest.scope, phase: manifest.phase, author: manifest.author, capturedAt: manifest.capturedAt,
    redEvidence: manifest.redEvidence ?? null, fileCount: shape.files.length, bindingCount: shape.bindingCount,
    files: shape.files, bindingPaths: shape.bindings, protectedRoots: shape.roots,
    manifestDigest: `sha256:${sha256(bytes)}`,
    oracleDigest: `sha256:${sha256(Buffer.concat([Buffer.from("aspm-oracle-v1\0"), bytes, signature, Buffer.from(authority.fingerprint)]))}`,
    authorityFingerprint: authority.fingerprint,
  };
}

export function treeDigest({ root, files } = {}) {
  const base = sourceRoot(root);
  const paths = pathList(files, "treeDigest files").sort();
  const hash = createHash("sha256").update("aspm-source-tree-v1\0");
  for (const name of paths) {
    const data = readRegular(sourcePath(base, name));
    const path = Buffer.from(name, "utf8");
    const lengths = Buffer.alloc(12);
    lengths.writeUInt32BE(path.length);
    lengths.writeBigUInt64BE(BigInt(data.length), 4);
    hash.update(lengths).update(path).update(data);
  }
  return `sha256:${hash.digest("hex")}`;
}

export function checkBinding(source, path) {
  if (typeof source !== "string" || Buffer.byteLength(source) > maximumFileBytes) fail("binding source must be bounded text");
  if (/^\s*\/\/\s*(?:go:|line\b|\+build\b)/m.test(source)) fail(`forbidden test/build directive in binding: ${path}`);
  const strings = [];
  const code = source.replace(/\/\*[\s\S]*?\*\/|\/\/[^\r\n]*|`[^`]*`|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'/g, (token) => {
    if (token.startsWith("//") || token.startsWith("/*") || token.startsWith("'")) return " ";
    let value;
    try { value = token.startsWith("`") ? token.slice(1, -1) : JSON.parse(token); }
    catch { fail(`unsupported escaped literal in forwarding binding: ${path}`); }
    strings.push(value);
    return ` __literal_${strings.length - 1} `;
  });
  if (!/^\s*package\s+[A-Za-z_]\w*/.test(code)) fail(`binding must be Go source: ${path}`);
  const imports = [...code.matchAll(/\bimport\s*(?:\(([\s\S]*?)\)|((?:[A-Za-z_]\w*|\.)?\s*__literal_\d+))/g)];
  for (const match of imports) {
    const declaration = match[1] ?? match[2];
    if (/(?:^|\s)[_.]\s+__literal_/.test(declaration)) fail(`forbidden side-effect or dot import in binding: ${path}`);
    for (const literal of declaration.matchAll(/__literal_(\d+)/g)) {
      const name = strings[Number(literal[1])];
      if (/^(?:os|io|net|database|runtime|unsafe|reflect|testing|syscall|plugin|C)(?:\/|$)/.test(name) ||
        /(?:^|\/)(?:sql|pgx|sqlite|sqlmock|httptest|testify|clock|mock)(?:\/|$)/.test(name)) {
        fail(`forbidden I/O or test-substitute import in binding: ${path}`);
      }
    }
  }
  const forbidden = [
    /\bfunc\s+(?:Test\w*|Benchmark\w*|Fuzz\w*|Example\w*)\s*\(/,
    /\b(?:Skip|Skipf|SkipNow|FailNow|Exit|Goexit|Setenv|Chdir|Cleanup|Parallel)\s*\(/,
    /\b(?:Query|QueryContext|QueryRow|QueryRowContext|Exec|ExecContext|Execute|Prepare|PrepareContext|BeginTx|Begin|Commit|Rollback)\s*\(/,
    /\b(?:ReadFile|WriteFile|ReadAll|WriteString|Mkdir|MkdirAll|CreateTemp|Create|Remove|RemoveAll|Rename|OpenFile|Stat|Lstat|Read|Write)\s*\(/,
    /\b(?:Dial|DialContext|Listen|ListenAndServe|Serve|RoundTrip|Do|WriteHeader|HandlerFunc|NewRecorder|NewServer)\s*\(/,
    /\b(?:Sleep|After|AfterFunc|Tick|NewTimer|NewTicker|Now|Marshal|Unmarshal|NewDecoder|NewEncoder)\s*\(/,
    /\b(?:ResponseWriter|ResponseRecorder)\b/,
    /\bgo\s+(?:func\b|[A-Za-z_])|\bselect\s*\{/,
    /\breturn\s+nil\s*,\s*nil\b/,
  ];
  if (forbidden.some((pattern) => pattern.test(code)) ||
    strings.some((value) => /^\s*(?:SELECT|INSERT|UPDATE|DELETE|CREATE\s+TABLE|DROP\s+TABLE|ALTER\s+TABLE|TRUNCATE)\b/i.test(value))) {
    fail(`forbidden suppression or substitute business logic in binding: ${path}`);
  }
  return { path, status: "forwarding-heuristic-only" };
}

export function captureOracle(options) {
  const { root, files, protectedRoots, bindingPaths, scope, phase, author, signingKeyPath, redEvidence } = options;
  if (!text(signingKeyPath)) fail("capture requires an explicit test-author signing-key reference");
  const base = sourceRoot(root);
  if (!Array.isArray(files) || files.length === 0) fail("capture requires an explicit test-author file list");
  const entries = files.map((entry) => {
    const value = typeof entry === "string" ? { path: entry, normalization: "none" } : entry;
    oraclePath(value?.path);
    const bytes = readRegular(sourcePath(base, value.path));
    return { path: value.path, normalization: value.normalization, sha256: sha256(normalized(bytes, value.normalization)) };
  });
  const manifest = { schemaVersion: 1, scope, phase, author, capturedAt: new Date().toISOString(), redEvidence: redEvidence ?? null, protectedRoots, bindingPaths, files: entries };
  checkManifestFiles(base, manifest);
  let key;
  try {
    key = createPrivateKey(readRegular(resolve(signingKeyPath), 65536));
    if (key.type !== "private" || key.asymmetricKeyType !== "ed25519") fail("Ed25519 signing key required");
  } catch { fail("explicit signing-key file is unavailable or is not an Ed25519 private key"); }
  const bytes = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`, "utf8");
  return { manifest, bytes, signature: Buffer.from(`${sign(null, bytes, key).toString("base64")}\n`) };
}

export function publishOracle(root, manifestPath, signaturePath, captured) {
  const base = sourceRoot(root);
  const names = pathList([manifestPath, signaturePath], "new oracle output paths");
  const written = [];
  try {
    for (const [index, name] of names.entries()) {
      const parts = name.split("/");
      let parent = base;
      for (const part of parts.slice(0, -1)) {
        parent = join(parent, part);
        if (!existsSync(parent)) mkdirSync(parent, { mode: 0o700 });
        const stat = lstatSync(parent);
        if (!stat.isDirectory() || stat.isSymbolicLink()) fail("oracle output parent must not be a symlink");
      }
      const path = join(base, ...parts);
      const fd = openSync(path, "wx", 0o600);
      written.push(path);
      try {
        writeFileSync(fd, index === 0 ? captured.bytes : captured.signature);
        fsyncSync(fd);
      } finally { closeSync(fd); }
    }
  } catch (error) {
    for (const path of written) unlinkSync(path);
    throw error;
  }
}

export function gitSourceFiles(root) {
  const base = sourceRoot(root);
  const result = spawnSync("git", ["-C", base, "ls-files", "-z", "--cached", "--others", "--exclude-standard"], { encoding: "utf8", maxBuffer: 16 * 1024 * 1024, windowsHide: true });
  if (result.error || result.status !== 0) fail("git source enumeration failed; supply an explicit source file list instead");
  return pathList([...new Set(result.stdout.split("\0").filter(Boolean))], "git source files").sort();
}

export function readOracleJSON(path) {
  if (!text(path)) fail("an explicit JSON configuration/file-list path is required");
  try { return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(readRegular(resolve(path), 4 * 1024 * 1024))); }
  catch { fail("oracle JSON configuration/file list is unavailable or invalid"); }
}

export function oracleFileDigest(path) {
  if (!text(path)) fail("an explicit digest input file is required");
  return `sha256:${sha256(readRegular(resolve(path)))}`;
}

export function authorityFingerprint(path) {
  return publicAuthority(path).fingerprint;
}

function compilerOrTestControl(path) {
  return /^(?:scripts\/|\.woodpecker\/|tests\/)/.test(path) ||
    /(?:^|\/)(?:__tests__|__snapshots__|__mocks__|tests|testdata|fixtures)\//.test(path) ||
    /(?:_test\.go|\.(?:test|spec)\.[cm]?[jt]sx?)$/.test(path) ||
    /(?:^|\/)(?:go\.(?:mod|sum|work)|go\.work\.sum|package(?:-lock)?\.json|tsconfig[^/]*\.json|jsconfig[^/]*\.json|(?:vite|vitest|jest|playwright|webpack|eslint|babel)\.config\.[cm]?[jt]s|\.gitignore|\.gitattributes|\.npmrc|Makefile|GNUmakefile)$/.test(path);
}

function guardSnapshot(options) {
  const { root, configPath, configDigest, trustedKeyPath, trustedKeyFingerprint } = options;
  const base = sourceRoot(root);
  if (!/^sha256:[a-f0-9]{64}$/.test(configDigest ?? "") || !/^sha256:[a-f0-9]{64}$/.test(trustedKeyFingerprint ?? "")) {
    fail("guard requires independently trusted configuration and authority fingerprints");
  }
  if (!text(configPath)) fail("an explicit oracle configuration path is required");
  const configBytes = readRegular(resolve(configPath), 4 * 1024 * 1024);
  if (`sha256:${sha256(configBytes)}` !== configDigest) fail("trusted oracle configuration digest mismatch");
  let config;
  try { config = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(configBytes)); }
  catch { fail("oracle configuration is not valid UTF-8 JSON"); }
  if (!config || config.schemaVersion !== 1 || !Array.isArray(config.oracles) || config.oracles.length < 1 || config.oracles.length > 256) {
    fail("required oracle authority/configuration is absent or invalid");
  }
  const requiredControls = pathList(config.requiredControls, "required guard/compiler/helper controls");
  const results = [];
  const manifestPaths = [];
  const scopes = new Set();
  for (const entry of config.oracles) {
    const manifestPath = sourcePath(base, oraclePath(entry?.manifest));
    const signaturePath = sourcePath(base, oraclePath(entry?.signature));
    const result = verifyOracle({ root: base, manifestPath, signaturePath, trustedKeyPath, trustedKeyFingerprint });
    if (scopes.has(result.scope)) fail("duplicate configured oracle scope");
    scopes.add(result.scope);
    results.push(result);
    manifestPaths.push(entry.manifest, entry.signature);
  }
  const files = pathList(typeof options.files === "function" ? options.files() : options.files, "tested source scope").sort();
  const sources = new Set(files);
  const protectedFiles = new Set(results.flatMap((result) => result.files));
  const bindings = new Set(results.flatMap((result) => result.bindingPaths));
  for (const name of [...protectedFiles, ...manifestPaths, ...requiredControls]) {
    if (!sources.has(name)) fail(`required oracle/control input omitted from tested tree: ${name}`);
  }
  for (const name of [...requiredControls, ...files.filter(compilerOrTestControl)]) {
    if (!protectedFiles.has(name) && !bindings.has(name)) fail(`unprotected guard/compiler/test/helper control: ${name}`);
  }
  results.sort((a, b) => a.scope < b.scope ? -1 : a.scope > b.scope ? 1 : 0);
  const oracleDigest = `sha256:${sha256(Buffer.from(JSON.stringify({
    configDigest, authority: trustedKeyFingerprint, oracles: results.map((result) => result.oracleDigest),
  })))}`;
  return {
    treeDigest: treeDigest({ root: base, files }), oracleDigest, fileCount: files.length,
    authorityFingerprint: trustedKeyFingerprint, configDigest,
    oracles: results.map(({ scope, phase, author, capturedAt, redEvidence, manifestDigest, oracleDigest }) =>
      ({ scope, phase, author, capturedAt, redEvidence, manifestDigest, oracleDigest })),
  };
}

export function oracleEnvironment(root = process.cwd()) {
  const required = (name) => {
    if (!text(process.env[name])) fail(`required ${name} is absent; verification cannot be skipped`);
    return process.env[name];
  };
  return {
    root, configPath: required("ASPM_ORACLE_CONFIG"),
    configDigest: required("ASPM_ORACLE_CONFIG_SHA256"),
    trustedKeyPath: required("ASPM_ORACLE_TRUSTED_KEY_FILE"),
    trustedKeyFingerprint: required("ASPM_ORACLE_TRUSTED_FINGERPRINT"),
    files: () => gitSourceFiles(root),
  };
}

export function withOracleGuard(options, operation) {
  if (typeof operation !== "function") fail("a guarded check operation is required");
  const before = guardSnapshot(options);
  console.log(JSON.stringify({ event: "oracle-before", ...before }));
  let value, operationError, after, verificationError;
  try { value = operation(); } catch (error) { operationError = error; }
  try {
    after = guardSnapshot(options);
    if (before.treeDigest !== after.treeDigest || before.oracleDigest !== after.oracleDigest) {
      fail("source tree or oracle inputs changed during checks; reject these results");
    }
    console.log(JSON.stringify({ event: "oracle-after", ...after }));
  } catch (error) { verificationError = error; }
  if (operationError && verificationError) throw new AggregateError([operationError, verificationError], "Oracle: command failed and post-check oracle/source verification also failed");
  if (verificationError) throw verificationError;
  if (operationError) throw operationError;
  console.log(JSON.stringify({ event: "tested-tree-verified", ...after, independentAcceptance: "required-separately" }));
  return value;
}
