import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));
const read = (...parts) => JSON.parse(readFileSync(join(root, ...parts), "utf8"));
const profile = read("infra", "reference-profile.json");
assert.equal(profile.status, "not-deployed");
assert.equal(profile.productionApplicationInstalled, false);
assert.equal(profile.secretsInConfiguration, false);
const tofu = read("infra", "tofu", "reference", "main.tf.json");
assert.equal(tofu.terraform.required_version, `= ${profile.candidateVersions.opentofu}`);
assert.equal(tofu.terraform.required_providers.libvirt.version, `= ${profile.candidateVersions.libvirtProvider}`);
assert.equal(tofu.variable.allow_provisioning.default, false);
assert.equal(tofu.resource.libvirt_volume.node.lifecycle.prevent_destroy, true);
assert.equal(tofu.resource.libvirt_pool.aspm_lab.lifecycle.prevent_destroy, true);
assert.ok(tofu.resource.libvirt_volume.node.lifecycle.precondition[0].condition.includes("filesha256"));
assert.equal(read("infra", "tofu", "reference", "reference.tfvars.example.json").allow_provisioning, false);
const inventory = read("infra", "ansible", "inventory.example.yml");
assert.equal(inventory.all.children.aspm_reference.vars.aspm_allow_host_changes, false);
for (const host of Object.values(inventory.all.children.aspm_reference.hosts)) assert.match(host.ansible_host, /^192\.0\.2\./);
const plays = read("infra", "ansible", "reference.yml");
assert.equal(plays[0].become, false);
for (const task of plays[0].tasks) {
  assert.equal("ansible.builtin.shell" in task, false, "Host preparation must not interpolate arbitrary shell.");
  if (task.become) assert.ok(task.when.includes("aspm_allow_host_changes"));
}
const verify = read(".woodpecker", "verify.yaml");
assert.ok(verify.when[0].event.includes("pull_request"));
assert.equal(JSON.stringify(verify).includes("from_secret"), false, "Untrusted contribution builds must have no secret bindings.");
const release = read(".woodpecker", "release.yaml");
assert.deepEqual(release.when, [{ event: "manual", branch: "main" }]);
assert.equal(release.labels["aspm-worker"], "isolated-release");
assert.equal(release.steps[0].environment.ASPM_RELEASE_APPROVED_REVISION.from_secret, "aspm_reviewed_revision");
assert.ok(release.steps[0].commands.some((command) => command.includes("scripts/sign.mjs sign")));
for (const workflow of [verify, release]) {
  for (const step of workflow.steps) {
    assert.ok(!step.image.endsWith(":latest"));
    assert.equal(step.privileged, undefined);
    assert.ok(!JSON.stringify(step).includes("docker.sock"));
  }
  const registry = readFileSync(join(root, "infra", "ci", "registry.container"), "utf8");
  const publishedPorts = registry.split(/\r?\n/).filter((line) => line.startsWith("PublishPort="));
  assert.deepEqual(publishedPorts, ["PublishPort=127.0.0.1:5000:5000"]);
  assert.ok(registry.includes("Image=registry:3.0.0"));
  assert.ok(registry.includes("Volume=aspm-reference-registry-data:/var/lib/registry"));
  const worker = readFileSync(join(root, "infra", "ci", "Containerfile"), "utf8");
  assert.ok(worker.includes(`golang:${profile.candidateVersions.go}-bookworm`));
  assert.ok(worker.includes(`node:${profile.candidateVersions.node}-bookworm`));
}
console.log("Reference IaC and CI JSON/YAML structure, version alignment, explicit opt-ins and secret separation passed.");
console.log("This is not tofu validate, Ansible syntax-check, CI worker execution or a deployment result.");
