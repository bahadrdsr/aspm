import { createHash } from "node:crypto";
import { readFileSync, realpathSync } from "node:fs";
import { isAbsolute, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";
import { build } from "vite";
import { requireProductionUI } from "./network";
import { requireApprovedProvider, requireSourceApproval, verifyApprovedLicense } from "./source-approval";
import type { SourceApprovalRegistry } from "./source-approval";

const root = fileURLToPath(new URL("..", import.meta.url));

interface SourceRecord {
  id: string;
  provider: "shadcn" | "animate-ui";
  localPath: string;
  localSHA256: string;
  upstream: {
    repository: string;
    commit: string;
    path: string;
    snapshotPath: string;
    sha256: string;
    license: string;
  };
  adaptation: "verbatim" | "reviewed-patch";
  patchRef?: string;
  reviewRef: string;
}

function readOwnedFile(path: string): { path: string; content: string; bytes: Uint8Array } {
  expect(isAbsolute(path) || path.includes("://"), "Source evidence must use web-relative paths.").toBe(false);
  const actual = realpathSync(resolve(root, path.replaceAll("\\", sep)));
  const within = relative(root, actual);
  expect(isAbsolute(within) || within === ".." || within.startsWith(`..${sep}`), "Source evidence must stay within web.").toBe(false);
  const bytes = readFileSync(actual);
  const content = bytes.toString("utf8").replaceAll("\r\n", "\n");
  expect(content.trim(), `${path} must contain reviewable source/evidence.`).not.toBe("");
  return { path: actual, content, bytes };
}

function normalizedPath(path: string): string {
  const normalized = path.replaceAll("\\", "/").split("?")[0].replace(/\/+$/, "");
  return process.platform === "win32" ? normalized.toLowerCase() : normalized;
}

function digest(content: string): string {
  return createHash("sha256").update(content).digest("hex");
}

test("production assets use independently approved source and shared Motion without test fixtures or credential canaries", async () => {
  const approvals = JSON.parse(readOwnedFile("tests/approved-component-sources.json").content) as SourceApprovalRegistry;
  requireApprovedProvider("animate-ui", approvals);
  requireApprovedProvider("shadcn", approvals);
  requireProductionUI();
  const manifest = JSON.parse(readOwnedFile("src/components/provenance.json").content) as {
    schemaVersion: number;
    primitiveFamily: string;
    components: SourceRecord[];
  };
  expect(manifest.schemaVersion).toBe(1);
  expect(["radix", "base-ui"]).toContain(manifest.primitiveFamily);
  expect(Array.isArray(manifest.components)).toBe(true);
  expect(manifest.components.length).toBeGreaterThanOrEqual(2);
  const localFiles = new Map<string, string[]>();
  const ids = new Set<string>();
  for (const record of manifest.components) {
    expect(ids.has(record.id), "Source records need unique IDs.").toBe(false);
    ids.add(record.id);
    expect(["shadcn", "animate-ui"]).toContain(record.provider);
    expect(record.upstream.repository).toBe(record.provider === "shadcn" ? "shadcn-ui/ui" : "imskyleen/animate-ui");
    expect(record.upstream.commit).toMatch(/^[a-f0-9]{40}$/);
    expect(record.upstream.path.trim()).not.toBe("");
    const approved = requireSourceApproval({
      provider: record.provider,
      repository: record.upstream.repository,
      commit: record.upstream.commit,
      path: record.upstream.path,
      sourceSHA256: record.upstream.sha256,
    }, approvals.approvedSources, approvals.licenseReviewedCandidates);
    expect(record.upstream.license).toBe(approved.license.expression);
    verifyApprovedLicense(readOwnedFile(approved.license.snapshotPath).bytes, approved);
    readOwnedFile(approved.approval.reviewRef);
    expect(record.localPath.replaceAll("\\", "/")).toMatch(/^src\/components\//);
    const local = readOwnedFile(record.localPath);
    const upstream = readOwnedFile(record.upstream.snapshotPath);
    expect(digest(local.content)).toBe(record.localSHA256);
    expect(digest(upstream.content)).toBe(record.upstream.sha256);
    readOwnedFile(record.reviewRef);
    if (record.adaptation === "verbatim") {
      expect(local.content).toBe(upstream.content);
    } else {
      expect(record.adaptation).toBe("reviewed-patch");
      expect(record.patchRef, "Adapted upstream code needs a reviewable patch, not a styling claim.").toBeTruthy();
      readOwnedFile(record.patchRef!);
    }
    const paths = localFiles.get(record.provider) ?? [];
    paths.push(normalizedPath(local.path));
    localFiles.set(record.provider, paths);
  }
  expect([...localFiles.keys()].sort()).toEqual(["animate-ui", "shadcn"]);

  const canary = "SYNTHETIC-M01-CANARY-NOT-A-REAL-CREDENTIAL";
  const variableNames = ["ASPM_DATABASE_PASSWORD", "ASPM_PROVIDER_API_KEY", "VITE_API_KEY"];
  const saved = variableNames.map((name) => [name, process.env[name]] as const);
  for (const name of variableNames) process.env[name] = canary;
  try {
    const result = await build({
      configFile: fileURLToPath(new URL("../vite.config.ts", import.meta.url)),
      logLevel: "silent",
      build: { write: false, emptyOutDir: false },
    });
    const outputs = Array.isArray(result) ? result : [result];
    const renderedModules = new Set<string>();
    let chunkCount = 0;
    for (const output of outputs) {
      if (!("output" in output)) throw new Error("Expected a completed static production build.");
      for (const artifact of output.output) {
        const content = artifact.type === "chunk" ? artifact.code : String(artifact.source);
        expect(content, "No backend secret or public-prefix credential canary may enter production assets.").not.toContain(canary);
        if (artifact.type !== "chunk") continue;
        chunkCount += 1;
        for (const [id, module] of Object.entries(artifact.modules)) {
          if (module.renderedLength > 0) renderedModules.add(normalizedPath(id));
          expect(normalizedPath(id).startsWith(`${normalizedPath(root)}/tests/`),
            "Production UI must not import acceptance fixtures or the harness as sample fallback data.").toBe(false);
        }
      }
    }
    expect(chunkCount).toBeGreaterThan(0);
    for (const provider of ["shadcn", "animate-ui"]) {
      expect(localFiles.get(provider)?.some((path) => renderedModules.has(path)),
        `${provider} source must be used in the shipped bundle, not parked unused beside handmade substitutes.`).toBe(true);
    }
    expect([...renderedModules].some((path) => /\/node_modules\/(?:motion|framer-motion)\//.test(path)),
      "The shared Motion runtime must back the motion behaviors; a similarly styled substitute is not sufficient.").toBe(true);
  } finally {
    for (const [name, value] of saved) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  }
});
