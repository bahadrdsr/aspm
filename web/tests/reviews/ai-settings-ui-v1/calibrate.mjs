import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../../../", import.meta.url));
const output = process.argv[2];
if (!output || !resolve(output).startsWith(resolve(root, ".artifacts", "ai-settings-ui-v1-author") + sep)) {
  throw new Error("Calibration output must stay in the owned author artifacts.");
}
const data = await import(new URL("../../ai-settings-ui-data.ts", import.meta.url).href);
const checks = [];
function check(name, run) { run(); checks.push({ name, passed: true }); }
check("Four declared profile families and distinct configured Foundry fields", () => {
  assert.deepEqual(data.families, ["openai", "azure-foundry", "anthropic", "local"]);
  for (const family of data.families) assert.equal(data.profileBodyValid(data.createInput(family)), true);
  const foundry = data.createInput("azure-foundry");
  assert.notEqual(foundry.model, foundry.deployment);
  assert.equal(data.profileBodyValid({ ...foundry, deployment: "" }), false);
  assert.equal(data.profileBodyValid({ ...data.createInput("openai"), deployment: "not-foundry" }), false);
});
const validEndpoints = [
  ["https://provider.synthetic.invalid/base/", "openai"], ["https://127.0.0.1:9443/exact-base/", "anthropic"],
  ["https://foundry.synthetic.invalid/operator-base", "azure-foundry"], ["https://local.synthetic.invalid/v1", "local"],
  ["http://127.0.0.1:8899/v1", "local"], ["http://10.23.45.67:8899/v1", "local"],
  ["http://172.16.1.2/v1", "local"], ["http://192.168.5.6/v1", "local"],
  ["http://[::1]:8899/v1", "local"], ["http://[fd12::1]:8899/v1", "local"],
  ["http://[::ffff:127.0.0.1]:8899/v1", "local"],
];
for (const [endpoint, family] of validEndpoints) {
  check(`Declared safe endpoint ${family}: ${endpoint}`, () => assert.equal(data.validEndpoint(endpoint, family), true));
}
const invalidEndpoints = [
  ["http://127.0.0.1:8899/v1", "openai"], ["http://localhost:8899/v1", "local"], ["http://8.8.8.8/v1", "local"],
  ["http://127.1:8899/v1", "local"], ["https://synthetic:canary@provider.synthetic.invalid/v1", "openai"],
  ["https://provider.synthetic.invalid/v1?route=other", "openai"], ["https://provider.synthetic.invalid/v1?", "openai"],
  ["https://provider.synthetic.invalid/v1#other", "openai"], ["https://provider.synthetic.invalid/v1#", "openai"],
  ["https://provider.synthetic.invalid/v1\n", "openai"], ["https://provider.synthetic.invalid/v1%0a", "openai"],
  ["https://provider.synthetic.invalid:70000/v1", "openai"], ["https://provider.synthetic.invalid:bad/v1", "openai"],
  ["https://provider.synthetic.invalid:/v1", "openai"], ["https://provider.synthetic.invalid:0/v1", "openai"],
  ["https://provider.synthetic.invalid/v1/../other", "openai"], ["https://provider.synthetic.invalid/v1/%2e%2e/other", "openai"],
  ["https://provider.synthetic.invalid/v1%2fother", "openai"], ["not-a-URL", "local"],
  [`https://provider.synthetic.invalid/${"x".repeat(16384)}`, "openai"],
];
for (const [index, [endpoint, family]] of invalidEndpoints.entries()) {
  check(`Invalid endpoint vector ${index + 1} (${family})`, () => assert.equal(data.validEndpoint(endpoint, family), false));
}
check("Combined PATCH validation and explicit sparse credential semantics", () => {
  assert.equal(data.profileBodyValid({ name: "A sparse name" }, data.hostedProfile), true);
  assert.equal(data.profileBodyValid({ apiKey: data.replacementKey }, data.hostedProfile), true);
  assert.equal(data.profileBodyValid({ apiKey: "" }, data.hostedProfile), false);
  assert.equal(data.profileBodyValid({ apiKey: null }, data.hostedProfile), false);
  assert.equal(data.profileBodyValid({ apiKey: null }, { ...data.localProfile, credentialConfigured: true }), true);
  assert.equal(data.profileBodyValid({ family: "openai", endpoint: "https://provider.synthetic.invalid/v1" }, data.localProfile), false);
  assert.equal(data.profileBodyValid({ family: "azure-foundry" }, data.hostedProfile), false);
  assert.equal(data.profileBodyValid({ family: "unknown" }, data.hostedProfile), false);
  assert.equal(data.profileBodyValid({ revision: "caller-authority" }, data.hostedProfile), false);
  assert.equal(data.profileBodyValid({ approvalRef: "caller-authority" }, data.hostedProfile), false);
  assert.equal(data.profileBodyValid({ ...data.createInput("openai"), apiKey: null }), false);
  assert.equal(data.profileBodyValid({ ...data.createInput("local"), apiKey: null }), true);
});
check("UTF-8 bounds, required booleans and no catalog capability assumption", () => {
  assert.equal(data.profileBodyValid({ ...data.createInput("local"), name: "安全".repeat(50) }), false);
  assert.equal(data.profileBodyValid({ ...data.createInput("openai"), apiKey: "x".repeat(16385) }), false);
  assert.equal(data.profileBodyValid({ ...data.createInput("local"), model: " " }), false);
  assert.equal(data.profileBodyValid({ ...data.createInput("local"), enabled: "true" }), false);
  assert.equal(data.profileBodyValid({ ...data.createInput("local"), structuredOutput: "true" }), false);
  assert.equal(data.profileBodyValid({ ...data.createInput("local"), structuredOutput: false }), true);
});
check("Native 100+1 pagination, max 500, exact lowercase-hex continuation and real empty pages", () => {
  const values = data.profileSeries(), initial = data.pageOf(values, new URL("http://127.0.0.1/api/v1/ai/profiles"));
  assert.equal(values.length, 101); assert.equal(initial.items.length, 100); assert.equal(initial.nextCursor, values[99].id);
  const tail = data.pageOf(values, new URL(`http://127.0.0.1/api/v1/ai/profiles?cursor=${initial.nextCursor}`));
  assert.deepEqual(tail, { apiVersion: data.apiVersion, items: values.slice(100), total: 101, nextCursor: null });
  assert.equal(data.pageOf(values, new URL("http://127.0.0.1/api/v1/ai/profiles?limit=500")).items.length, 101);
  assert.equal(data.pageOf(values, new URL("http://127.0.0.1/api/v1/ai/profiles?limit=1")).nextCursor, values[0].id);
  assert.deepEqual(data.pageOf([], new URL("http://127.0.0.1/api/v1/ai/profiles")), { apiVersion: data.apiVersion, items: [], total: 0, nextCursor: null });
  for (const query of ["limit=0", "limit=501", "limit=2.5", "cursor=not-an-ID", "cursor=C8000000000000000000000000000001",
    "cursor=a&cursor=b", "limit=1&limit=2", "offset=100", "workspaceId=forged"]) {
    assert.throws(() => data.pageParameters(new URL(`http://127.0.0.1/api/v1/ai/profiles?${query}`)));
  }
});
check("Exact nonsecret lower-camel metadata and opaque revisions", () => {
  assert.deepEqual(data.defaultPolicy(), { workspaceId: data.aiAlpha.id, mode: "disabled", revision: "0", updatedAt: null, updatedBy: null });
  assert.equal(data.backendID(data.hostedProfile.id), true);
  assert.equal(typeof data.hostedProfile.revision, "string");
  assert.equal(Number.isNaN(Number(data.hostedProfile.revision)), true);
  assert.equal(Number.isNaN(Number(data.refreshedProfile.revision)), true);
  assert.equal(data.refreshedProfile.revision < data.hostedProfile.revision, true);
  assert.equal(Object.hasOwn(data.hostedProfile, "apiKey"), false);
  const grant = data.grantFor();
  assert.equal(grant.destination, data.hostedProfile.endpoint);
  assert.equal(grant.destination.endsWith("/"), true);
  assert.equal(grant.task, "finding-validity"); assert.equal(grant.dataClass, "finding-evidence");
  assert.equal(grant.revokedAt, null); assert.equal(grant.revokedBy, null);
  assert.equal(Object.hasOwn(grant, "approvalRef"), false);
  assert.equal(data.timestamp(grant.expiresAt), true); assert.ok(Date.parse(grant.expiresAt) > Date.now());
});
const result = {
  schemaVersion: 1, capturedAt: new Date().toISOString(), outcome: "passed", checks,
  scope: "Pure synthetic data/protocol-shape calibration only. No browser UI, native API, HTTP, DB, provider, resolver, worker or encryption execution.",
  cannotClaim: "These checks do not execute assertions blocked after the missing AI settings entry.",
};
writeFileSync(output, JSON.stringify(result, null, 2) + "\n");
console.log(`${checks.length} pure fixture/data checks passed; no network or browser behavior was calibrated.`);
