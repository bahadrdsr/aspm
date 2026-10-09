import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash, createPublicKey } from "node:crypto";
import {
  copyFileSync, cpSync, createReadStream, existsSync, lstatSync, mkdirSync,
  readFileSync, readdirSync, realpathSync, rmSync, statSync, writeFileSync,
} from "node:fs";
import { dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import {
  canonicalJSON, deterministicTarGzip, digest, digestHex, privateKey, publicKey,
  readTar, readTarGzip, signBytes, validateReleaseConfiguration, verifyBytes,
} from "./release-lib.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const configPath = join(root, "release", "technical-preview.json");
const configBytes = readFileSync(configPath);
const config = validateReleaseConfiguration(JSON.parse(configBytes));
const output = join(root, ".artifacts", "release", config.version);
const work = join(output, ".work");
const releasePattern = /^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*)?$/;
const digestPattern = /^sha256:[a-f0-9]{64}$/;
const executables = [
  "core-api", "ingestion", "retention-worker", "report-worker", "delivery-worker",
  "collection-worker", "assessment-worker", "verification-worker", "aspmctl",
];

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd ?? root,
    env: { ...process.env, ...options.env },
    encoding: "utf8",
    windowsHide: true,
    timeout: options.timeout ?? 30 * 60_000,
    maxBuffer: 16 * 1024 * 1024,
  });
  if (result.error) throw result.error;
  assert.equal(result.status, 0, `${command} failed: ${result.stderr.trim()}`);
  return result.stdout.trim();
}

function git(args) {
  return run("git", ["--no-pager", ...args]);
}

function assertCleanRevision() {
  const revision = git(["rev-parse", "HEAD"]);
  assert.match(revision, /^[a-f0-9]{40}$/);
  assert.equal(git(["status", "--porcelain"]), "", "Release commands require a clean checkout.");
  return revision;
}

function safeOutput() {
  const expected = resolve(root, ".artifacts", "release", config.version);
  assert.equal(resolve(output), expected);
  assert.ok(expected.startsWith(resolve(root, ".artifacts", "release") + sep));
  return expected;
}

function regular(path) {
  const info = lstatSync(path);
  assert.ok(info.isFile() && !info.isSymbolicLink() && info.nlink === 1, `Expected one regular file: ${path}`);
  return info;
}

function copyTree(source, destination) {
  const info = lstatSync(source);
  assert.ok(info.isDirectory() && !info.isSymbolicLink(), `Expected plain directory: ${source}`);
  mkdirSync(destination, { recursive: true });
  for (const entry of readdirSync(source, { withFileTypes: true }).sort((a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0)) {
    const from = join(source, entry.name);
    const to = join(destination, entry.name);
    if (entry.isDirectory()) copyTree(from, to);
    else {
      assert.ok(entry.isFile() && !entry.isSymbolicLink(), `Unsupported source entry: ${from}`);
      copyFileSync(from, to);
    }
  }
}

function walk(directory, prefix = "") {
  const result = [];
  for (const entry of readdirSync(directory, { withFileTypes: true }).sort((a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0)) {
    const path = join(directory, entry.name);
    const name = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) result.push(...walk(path, name));
    else {
      assert.ok(entry.isFile() && !entry.isSymbolicLink(), `Unsupported staged entry: ${path}`);
      result.push({ path: name.replaceAll("\\", "/"), bytes: readFileSync(path) });
    }
  }
  return result;
}

function records(entries) {
  return entries.map((entry) => ({
    path: entry.path, sizeBytes: entry.bytes.length, sha256: digest(entry.bytes),
  }));
}

function assertRecords(directory, expected) {
  assert.deepEqual(records(walk(directory)), expected);
}

function parseJSONStream(text) {
  const values = [];
  let start = -1;
  let depth = 0;
  let string = false;
  let escape = false;
  for (let index = 0; index < text.length; index++) {
    const value = text[index];
    if (string) {
      if (escape) escape = false;
      else if (value === "\\") escape = true;
      else if (value === "\"") string = false;
      continue;
    }
    if (value === "\"") string = true;
    else if (value === "{") {
      if (depth === 0) start = index;
      depth++;
    } else if (value === "}") {
      depth--;
      if (depth === 0) values.push(JSON.parse(text.slice(start, index + 1)));
    }
  }
  assert.equal(depth, 0);
  return values;
}

function goComponents(go) {
  const modules = parseJSONStream(run(go, ["list", "-mod=readonly", "-m", "-json", "all"]));
  const sums = new Map();
  for (const line of readFileSync(join(root, "go.sum"), "utf8").trim().split(/\r?\n/)) {
    const [name, version, hash] = line.split(" ");
    if (!version.endsWith("/go.mod")) sums.set(`${name}@${version}`, hash);
  }
  return modules.filter((module) => !module.Main).map((module) => {
    const version = module.Replace?.Version ?? module.Version;
    const name = module.Replace?.Path ?? module.Path;
    const value = {
      type: "library", "bom-ref": `pkg:golang/${name}@${version}`,
      name, version, purl: `pkg:golang/${name}@${version}`, scope: "required",
    };
    const sum = sums.get(`${name}@${version}`);
    if (sum?.startsWith("h1:")) {
      value.hashes = [{ alg: "SHA-256", content: Buffer.from(sum.slice(3), "base64").toString("hex") }];
    }
    return value;
  });
}

function buildSBOM(go, applicationImage) {
  const web = JSON.parse(readFileSync(join(root, "web", "dist", "notices", "sbom.cdx.json")));
  const components = [
    ...goComponents(go),
    ...web.components,
    ...Object.entries(config.baseImages).map(([name, value]) => ({
      type: "container", "bom-ref": `aspm:base-image:${name}`,
      name: `${name}-base-image`, version: value.split("@")[0].split(":").at(-1),
      hashes: [{ alg: "SHA-256", content: value.split("@sha256:")[1] }],
      properties: [{ name: "aspm:image-reference", value }],
    })),
    {
      type: "container", "bom-ref": `aspm:application-image:${config.version}`,
      name: "aspm", version: config.version,
      hashes: [{ alg: "SHA-256", content: applicationImage.split("@sha256:")[1] }],
      properties: [{ name: "aspm:image-reference", value: applicationImage }],
    },
  ];
  components.sort((left, right) => left["bom-ref"] < right["bom-ref"] ? -1 : left["bom-ref"] > right["bom-ref"] ? 1 : 0);
  const serial = digestHex(configBytes);
  return {
    bomFormat: "CycloneDX", specVersion: "1.6",
    serialNumber: `urn:uuid:${serial.slice(0, 8)}-${serial.slice(8, 12)}-4${serial.slice(13, 16)}-8${serial.slice(17, 20)}-${serial.slice(20, 32)}`,
    version: 1,
    metadata: {
      component: {
        type: "application", name: "aspm", version: config.version,
        licenses: [{ license: { id: "Apache-2.0" } }],
      },
      properties: [
        { name: "aspm:platform", value: config.platform },
        { name: "aspm:scope", value: "release payload, OCI image and locked Go/npm dependency graphs" },
      ],
    },
    components,
  };
}

function patchDeploymentFiles(destination) {
  copyTree(join(root, "deploy"), destination);
  const chart = join(destination, "helm", "aspm", "Chart.yaml");
  let chartText = readFileSync(chart, "utf8");
  chartText = chartText.replace(/^version: .+$/m, `version: ${config.version}`)
    .replace(/^appVersion: .+$/m, `appVersion: ${config.version}`);
  writeFileSync(chart, chartText);
  const values = join(destination, "helm", "aspm", "values.yaml");
  let valuesText = readFileSync(values, "utf8");
  valuesText = valuesText.replace(/^  tag: .+$/m, `  tag: ${config.version}`);
  writeFileSync(values, valuesText);
}

function prepare() {
  const revision = assertCleanRevision();
  const releaseRoot = safeOutput();
  if (existsSync(releaseRoot)) {
    const info = lstatSync(releaseRoot);
    assert.ok(info.isDirectory() && !info.isSymbolicLink(), "Refusing to replace a non-directory release output.");
    rmSync(releaseRoot, { recursive: true, force: false });
  }
  mkdirSync(work, { recursive: true });
  const go = process.env.ASPM_RELEASE_GO || "go";
  const npmCLI = process.env.ASPM_RELEASE_NPM_CLI ||
    join(dirname(process.execPath), "node_modules", "npm", "bin", "npm-cli.js");
  const docker = process.env.ASPM_RELEASE_DOCKER || "docker";
  regular(npmCLI);
  const npm = (args, options = {}) => run(process.execPath, [npmCLI, ...args], options);
  npm(["run", "build", "--silent"], { cwd: join(root, "web") });

  const payloadRoot = join(work, "payload", `aspm-${config.version}`);
  const bin = join(payloadRoot, "bin");
  mkdirSync(bin, { recursive: true });
  for (const name of executables) {
    const args = ["build", "-mod=readonly", "-trimpath", "-buildvcs=false"];
    if (name === "aspmctl") args.push("-ldflags", `-X main.version=${config.version}`);
    args.push("-o", join(bin, name), `.${sep}${join("cmd", name)}`);
    run(go, args, { env: { GOOS: "linux", GOARCH: "amd64", CGO_ENABLED: "0" } });
  }
  copyTree(join(root, "web", "dist"), join(payloadRoot, "web"));
  for (const name of ["LICENSE", "NOTICE"]) copyFileSync(join(root, name), join(payloadRoot, name));
  const goRoot = run(go, ["env", "GOROOT"]);
  copyFileSync(join(goRoot, "LICENSE"), join(payloadRoot, "Go-LICENSE.txt"));
  mkdirSync(join(payloadRoot, "docs"), { recursive: true });
  for (const name of ["m13-backup-restore.md", "m13-upgrade-rollback.md", "m13-verification-worker.md"]) {
    copyFileSync(join(root, "docs", name), join(payloadRoot, "docs", name));
  }

  const epoch = run("git", ["show", "-s", "--format=%ct", revision]);
  assert.match(epoch, /^[0-9]+$/);
  const oci = join(work, `aspm-${config.version}-linux-amd64.oci.tar`);
  const metadataPath = join(work, "oci-metadata.json");
  run(docker, [
    "buildx", "build", "--platform", "linux/amd64",
    "--file", join(root, "Containerfile"),
    "--tag", `${config.imageRepository}:${config.version}`,
    "--build-arg", `ASPM_VERSION=${config.version}`,
    "--build-arg", `ASPM_REVISION=${revision}`,
    "--provenance=mode=max", "--sbom=true",
    "--output", `type=oci,dest=${oci},rewrite-timestamp=true`,
    "--metadata-file", metadataPath, root,
  ], { env: { SOURCE_DATE_EPOCH: epoch }, timeout: 60 * 60_000 });
  const metadata = JSON.parse(readFileSync(metadataPath));
  const imageDigest = metadata["containerimage.digest"];
  assert.match(imageDigest, digestPattern);
  const applicationImage = `${config.imageRepository}:${config.version}@${imageDigest}`;

  const installerRoot = join(work, "installer");
  patchDeploymentFiles(installerRoot);
  const installerEntries = walk(installerRoot);
  const installerManifest = {
    apiVersion: "aspm/v1alpha1", kind: "InstallerBundle", release: config.version,
    files: Object.fromEntries(installerEntries.map((entry) => [entry.path, digest(entry.bytes)])),
    images: {
      application: applicationImage,
      postgres: config.postgresImage,
      storage: config.storageImage,
    },
  };
  writeFileSync(join(installerRoot, "manifest.json"), canonicalJSON(installerManifest), { flag: "wx" });

  const sbom = buildSBOM(go, applicationImage);
  writeFileSync(join(work, "sbom.cdx.json"), canonicalJSON(sbom), { flag: "wx" });
  const receipt = {
    schemaVersion: 1, kind: "ReleaseBuildReceipt",
    version: config.version, platform: config.platform,
    sourceRevision: revision, sourceEpoch: Number(epoch),
    configurationSHA256: digest(configBytes),
    applicationImage, imageDigest,
    tools: {
      go: run(go, ["version"]), node: process.version, npm: npm(["--version"]),
      docker: run(docker, ["version", "--format", "{{.Client.Version}}/{{.Server.Version}}"]),
      buildx: run(docker, ["buildx", "version"]),
    },
    payload: records(walk(join(work, "payload"))),
    installer: records(walk(installerRoot)),
    sbomSHA256: digest(readFileSync(join(work, "sbom.cdx.json"))),
    ociSHA256: digest(readFileSync(oci)),
  };
  writeFileSync(join(work, "build-receipt.json"), canonicalJSON(receipt), { flag: "wx" });
  console.log(`Prepared release ${config.version} from ${revision}. Signing and finalization remain required.`);
}

function releaseEntry(path) {
  const bytes = readFileSync(join(output, path));
  return { path, sizeBytes: bytes.length, sha256: digest(bytes) };
}

function finalize() {
  const revision = assertCleanRevision();
  const approved = process.env.ASPM_RELEASE_APPROVED_REVISION;
  assert.match(approved ?? "", /^[a-f0-9]{40}$/, "Protected reviewed revision approval is required.");
  assert.equal(revision, approved, "Reviewed revision does not match this checkout.");
  const keyPath = process.env.ASPM_SIGNING_KEY_FILE;
  assert.ok(keyPath && isAbsolute(keyPath), "ASPM_SIGNING_KEY_FILE must be an absolute protected key file.");
  const keyInfo = regular(keyPath);
  if (process.platform !== "win32") assert.equal(keyInfo.mode & 0o077, 0, "Signing key must be owner-only.");
  const key = privateKey(readFileSync(keyPath));
  const receipt = JSON.parse(readFileSync(join(work, "build-receipt.json")));
  assert.equal(receipt.sourceRevision, revision);
  assert.equal(receipt.version, config.version);
  assert.equal(receipt.configurationSHA256, digest(configBytes));
  assertRecords(join(work, "payload"), receipt.payload);
  assertRecords(join(work, "installer"), receipt.installer);
  assert.equal(digest(readFileSync(join(work, "sbom.cdx.json"))), receipt.sbomSHA256);
  const ociSource = join(work, `aspm-${config.version}-linux-amd64.oci.tar`);
  assert.equal(digest(readFileSync(ociSource)), receipt.ociSHA256);

  const installerManifestPath = join(work, "installer", "manifest.json");
  writeFileSync(join(work, "installer", "manifest.sig"),
    signBytes(readFileSync(installerManifestPath), key), { flag: "wx" });

  const binaryName = `aspm-${config.version}-linux-amd64.tar.gz`;
  const installerName = `aspm-${config.version}-installer-bundle.tar.gz`;
  const ociName = `aspm-${config.version}-linux-amd64.oci.tar`;
  const sbomName = `aspm-${config.version}-sbom.cdx.json`;
  const provenanceName = `aspm-${config.version}-provenance.json`;
  writeFileSync(join(output, binaryName), deterministicTarGzip(walk(join(work, "payload"))), { flag: "wx" });
  writeFileSync(join(output, installerName), deterministicTarGzip(
    walk(join(work, "installer")).map((entry) => ({
      ...entry, path: `aspm-${config.version}-installer/${entry.path}`,
    })),
  ), { flag: "wx" });
  copyFileSync(ociSource, join(output, ociName));
  copyFileSync(join(work, "sbom.cdx.json"), join(output, sbomName));
  const publicBytes = createPublicKey(key).export({ type: "spki", format: "pem" });
  writeFileSync(join(output, "release-public-key.pem"), publicBytes, { flag: "wx" });

  const subjects = [binaryName, installerName, ociName, sbomName, "release-public-key.pem"].map(releaseEntry);
  const provenance = {
    _type: "https://in-toto.io/Statement/v1",
    subject: subjects.map((entry) => ({
      name: entry.path, digest: { sha256: entry.sha256.slice("sha256:".length) },
    })),
    predicateType: "https://slsa.dev/provenance/v1",
    predicate: {
      buildDefinition: {
        buildType: "https://github.com/bahadrdsr/aspm/release-build/v1",
        externalParameters: {
          version: config.version, platform: config.platform,
          applicationImage: receipt.applicationImage,
          postgresImage: config.postgresImage, storageImage: config.storageImage,
        },
        internalParameters: {
          configurationSHA256: receipt.configurationSHA256,
          tools: receipt.tools, baseImages: config.baseImages,
        },
        resolvedDependencies: [{
          uri: "git+https://github.com/bahadrdsr/aspm.git",
          digest: { gitCommit: revision },
        }],
      },
      runDetails: {
        builder: { id: "https://github.com/bahadrdsr/aspm/.woodpecker/release.yaml" },
        metadata: { invocationId: `${revision}:${config.version}`, startedOn: null, finishedOn: null },
        byproducts: [],
      },
    },
  };
  writeFileSync(join(output, provenanceName), canonicalJSON(provenance), { flag: "wx" });

  const sumFiles = [...subjects.map((entry) => entry.path), provenanceName].sort();
  const sums = sumFiles.map((name) => `${digestHex(readFileSync(join(output, name)))}  ${name}\n`).join("");
  writeFileSync(join(output, "SHA256SUMS"), sums, { flag: "wx" });
  const files = [...sumFiles, "SHA256SUMS"].sort().map(releaseEntry);
  const manifest = {
    apiVersion: "aspm.dev/release/v1alpha1", kind: "ReleaseArtifactManifest",
    version: config.version, platform: config.platform,
    sourceRevision: revision, applicationImage: receipt.applicationImage,
    schemaVersion: 27, productionReady: false, technicalPreview: true,
    files,
    limitations: [
      "single-instance-technical-preview", "no-stateful-ha", "no-zero-downtime-upgrade",
      "no-database-down-migration", "external-trust-key-required",
      "accessibility-performance-and-capacity-qualified-separately",
    ],
  };
  const manifestBytes = canonicalJSON(manifest);
  writeFileSync(join(output, "release-manifest.json"), manifestBytes, { flag: "wx" });
  writeFileSync(join(output, "release-manifest.sig"), signBytes(manifestBytes, key), { flag: "wx" });
  rmSync(work, { recursive: true, force: false });
  console.log(`Finalized signed technical preview assets in ${output}. No registry or forge publication was performed.`);
}

function ensureExternalTrust(path) {
  assert.ok(path && isAbsolute(path), "Verification requires an absolute independently trusted public key.");
  const releaseRoot = realpathSync(output);
  const trust = realpathSync(path);
  const within = relative(releaseRoot, trust);
  assert.ok(within === ".." || within.startsWith(`..${sep}`) || isAbsolute(within),
    "A public key shipped inside the release cannot establish independent trust.");
  return publicKey(readFileSync(trust));
}

function verifyRelease(trustedKeyPath) {
  const key = ensureExternalTrust(trustedKeyPath);
  const manifestBytes = readFileSync(join(output, "release-manifest.json"));
  const manifest = JSON.parse(manifestBytes);
  assert.deepEqual(canonicalJSON(manifest), manifestBytes, "Release manifest is not canonical JSON.");
  verifyBytes(manifestBytes, readFileSync(join(output, "release-manifest.sig")), key);
  assert.equal(manifest.apiVersion, "aspm.dev/release/v1alpha1");
  assert.equal(manifest.kind, "ReleaseArtifactManifest");
  assert.equal(manifest.version, config.version);
  assert.equal(manifest.platform, config.platform);
  assert.equal(manifest.productionReady, false);
  assert.equal(manifest.technicalPreview, true);
  assert.match(manifest.applicationImage, new RegExp(`^${config.imageRepository.replaceAll(".", "\\.")}:${config.version.replaceAll(".", "\\.")}@sha256:[a-f0-9]{64}$`));
  const names = [];
  for (const entry of manifest.files) {
    assert.match(entry.path, /^[A-Za-z0-9][A-Za-z0-9_.-]*$/);
    const info = regular(join(output, entry.path));
    assert.equal(info.size, entry.sizeBytes);
    assert.equal(digest(readFileSync(join(output, entry.path))), entry.sha256);
    names.push(entry.path);
  }
  assert.equal(new Set(names).size, names.length);
  const sums = readFileSync(join(output, "SHA256SUMS"), "utf8");
  const expectedSums = names.filter((name) => name !== "SHA256SUMS").sort()
    .map((name) => `${digestHex(readFileSync(join(output, name)))}  ${name}\n`).join("");
  assert.equal(sums, expectedSums);
  assert.deepEqual(readFileSync(join(output, "release-public-key.pem")),
    createPublicKey(key).export({ type: "spki", format: "pem" }));

  const binaryName = `aspm-${config.version}-linux-amd64.tar.gz`;
  const binary = readTarGzip(readFileSync(join(output, binaryName)));
  for (const name of executables) assert.ok(binary.has(`aspm-${config.version}/bin/${name}`), `Missing binary ${name}.`);
  for (const name of ["LICENSE", "NOTICE", "Go-LICENSE.txt", "web/index.html"]) {
    assert.ok(binary.has(`aspm-${config.version}/${name}`), `Missing payload ${name}.`);
  }
  const installerName = `aspm-${config.version}-installer-bundle.tar.gz`;
  const installer = readTarGzip(readFileSync(join(output, installerName)));
  const prefix = `aspm-${config.version}-installer/`;
  const installerManifestBytes = installer.get(prefix + "manifest.json");
  const installerSignature = installer.get(prefix + "manifest.sig");
  assert.ok(installerManifestBytes && installerSignature);
  verifyBytes(installerManifestBytes, installerSignature, key);
  const installerManifest = JSON.parse(installerManifestBytes);
  assert.deepEqual(canonicalJSON(installerManifest), installerManifestBytes);
  assert.equal(installerManifest.release, config.version);
  assert.equal(installerManifest.images.application, manifest.applicationImage);
  assert.equal(installerManifest.images.postgres, config.postgresImage);
  assert.equal(installerManifest.images.storage, config.storageImage);
  for (const [name, expected] of Object.entries(installerManifest.files)) {
    assert.equal(digest(installer.get(prefix + name)), expected, `Installer artifact changed: ${name}`);
  }
  const oci = readTar(readFileSync(join(output, `aspm-${config.version}-linux-amd64.oci.tar`)));
  const index = JSON.parse(oci.get("index.json"));
  assert.ok(index.manifests.some((entry) => `sha256:${entry.digest.split(":").at(-1)}` === manifest.applicationImage.split("@")[1]),
    "OCI index does not contain the declared application image digest.");
  const sbom = JSON.parse(readFileSync(join(output, `aspm-${config.version}-sbom.cdx.json`)));
  assert.equal(sbom.bomFormat, "CycloneDX");
  assert.equal(sbom.specVersion, "1.6");
  assert.ok(sbom.components.length > 20);
  const provenance = JSON.parse(readFileSync(join(output, `aspm-${config.version}-provenance.json`)));
  assert.equal(provenance._type, "https://in-toto.io/Statement/v1");
  assert.equal(provenance.predicateType, "https://slsa.dev/provenance/v1");
  assert.equal(provenance.predicate.buildDefinition.resolvedDependencies[0].digest.gitCommit, manifest.sourceRevision);
  console.log(`Verified ${manifest.files.length} signed release assets for ${manifest.version}.`);
}

function selfTest() {
  const fixture = [
    { path: "bin/aspmctl", bytes: Buffer.from("synthetic executable\n"), mode: 0o755 },
    { path: "NOTICE", bytes: Buffer.from("synthetic notice\n") },
  ];
  const first = deterministicTarGzip(fixture);
  const second = deterministicTarGzip([...fixture].reverse());
  assert.deepEqual(first, second);
  assert.deepEqual([...readTarGzip(first).keys()], ["NOTICE", "bin/aspmctl"]);
  console.log("Deterministic release archive self-test passed.");
}

const [action, ...args] = process.argv.slice(2);
if (action === "prepare" && args.length === 0) prepare();
else if (action === "finalize" && args.length === 0) finalize();
else if (action === "verify" && args.length === 2 && args[0] === "--trusted-key") verifyRelease(resolve(args[1]));
else if (action === "self-test" && args.length === 0) selfTest();
else throw new Error("Use prepare, finalize, verify --trusted-key <absolute-public-key>, or self-test.");
