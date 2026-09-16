package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func minimalInstallation(target object) object {
	return object{
		"apiVersion": apiVersion, "kind": "Installation",
		"release": object{"version": "0.0.0-dev.1", "artifactSource": "installer-bundle"},
		"target":  target, "access": object{"baseURL": "https://aspm.example.invalid"},
	}
}

func plan(t *testing.T, input object) InstallationPlan {
	t.Helper()
	if Production.Plan == nil {
		t.Fatal("production binding missing: Plan; coder must forward to the installer configuration planner")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := Production.Plan(ctx, encode(t, input))
	ok(t, "plan installation", err)
	if p.ID == "" || !json.Valid(p.Configuration) {
		t.Fatal("successful planning needs a nonempty identity and resolved JSON configuration")
	}
	return p
}

func configuration(t *testing.T, p InstallationPlan) object {
	t.Helper()
	var value object
	ok(t, "decode resolved installation", json.Unmarshal(p.Configuration, &value))
	return value
}

func at(t *testing.T, value object, path ...string) any {
	t.Helper()
	var result any = value
	for _, key := range path {
		parent, isObject := result.(map[string]any)
		if !isObject {
			t.Fatalf("configuration path %v is not an object", path)
		}
		var present bool
		result, present = parent[key]
		if !present {
			t.Fatalf("configuration path %v is missing", path)
		}
	}
	return result
}

func TestM03_MinimalLinuxAndKubernetesPlans(t *testing.T) {
	for _, target := range []object{
		{"kind": "linux", "host": "localhost"},
		{"kind": "kubernetes", "context": "acceptance-cluster", "namespace": "aspm-acceptance"},
	} {
		p := plan(t, minimalInstallation(target))
		c := configuration(t, p)
		equal(t, "version", at(t, c, "apiVersion"), any(apiVersion))
		for key, value := range target {
			equal(t, "explicit target "+key, at(t, c, "target", key), value)
		}
		exposure := "private"
		if target["kind"] == "linux" {
			exposure = "loopback"
		}
		equal(t, "safe exposure", at(t, c, "access", "exposure"), any(exposure))
		equal(t, "TLS default", at(t, c, "access", "tlsMode"), any("internal-ca"))
		equal(t, "no public approval", at(t, c, "access", "publicExposureApproved"), any(false))
		equal(t, "AI disabled", at(t, c, "ai", "mode"), any("disabled"))
		equal(t, "proof disabled", at(t, c, "proof", "enabled"), any(false))
		equal(t, "bootstrap", at(t, c, "bootstrap", "method"), any("one-time-enrollment"))
		for _, store := range []string{"postgres", "objectStore"} {
			equal(t, store+" private", at(t, c, store, "publicAccess"), any(false))
			equal(t, store+" persistent", at(t, c, store, "persistent"), any(true))
			equal(t, store+" managed", at(t, c, store, "mode"), any("managed"))
			equal(t, store+" secret reference", at(t, c, store, "credentialRef", "kind"), any("generated"))
		}
		for _, service := range []string{"core-api", "ingestion-parser", "ingestion-reconciler", "background-worker"} {
			equal(t, service+" replica default", at(t, c, "services", service, "replicas"), any(float64(1)))
		}
		equal(t, "signed artifacts", at(t, c, "artifacts", "signatureRequired"), any(true))
		equal(t, "resolved input has same plan", plan(t, c).ID, p.ID)
	}
}

func TestM03_ReferencesAreRedactedAndPlanIdentityIsStable(t *testing.T) {
	canary := secret(t)
	t.Setenv("ASPM_ACCEPTANCE_ENROLLMENT", canary)
	input := minimalInstallation(object{"kind": "linux", "host": "localhost"})
	input["bootstrap"] = object{"method": "one-time-enrollment",
		"secretRef": object{"kind": "environment", "name": "ASPM_ACCEPTANCE_ENROLLMENT"}}
	first := plan(t, input)
	equal(t, "repeat identity", plan(t, input).ID, first.ID)
	equal(t, "reference retained", at(t, configuration(t, first), "bootstrap", "secretRef", "name"), any("ASPM_ACCEPTANCE_ENROLLMENT"))
	if bytes.Contains(encode(t, first), []byte(canary)) {
		t.Fatal("the plan exposed a resolved secret")
	}
	input["target"] = object{"kind": "linux", "host": "another-owned-host"}
	if plan(t, input).ID == first.ID {
		t.Fatal("a changed target reused the prior plan identity")
	}
	input["target"] = object{"kind": "linux", "host": "localhost"}
	input["bootstrap"] = object{"method": "one-time-enrollment",
		"secretRef": object{"kind": "generated", "name": "another-enrollment-reference"}}
	if plan(t, input).ID == first.ID {
		t.Fatal("a changed secret reference reused the prior plan identity")
	}
}

func TestM03_InvalidTargetsAndUnsafeAccessAreRejected(t *testing.T) {
	if Production.Plan == nil {
		t.Fatal("production binding missing: Plan")
	}
	cases := []object{
		{"target": object{"kind": "windows", "host": "localhost"}},
		{"target": object{"kind": "linux"}},
		{"target": object{"kind": "kubernetes", "namespace": "aspm"}},
		{"target": object{"kind": "kubernetes", "context": "owned-cluster"}},
		{"access": object{"baseURL": "http://aspm.example.invalid"}},
		{"access": object{"baseURL": "https://aspm.example.invalid", "exposure": "public", "publicExposureApproved": false}},
		{"postgres": object{"publicAccess": true}},
		{"objectStore": object{"publicAccess": true}},
		{"objectStore": object{"mode": "external", "engine": "s3-compatible", "api": "s3", "persistent": true,
			"publicAccess": false, "endpoint": "http://storage.example.invalid", "bucket": "evidence",
			"credentialRef": object{"kind": "environment", "name": "ASPM_ACCEPTANCE_STORAGE"}}},
		{"bootstrap": object{"method": "one-time-enrollment", "password": "synthetic-not-a-credential"}},
	}
	for i, override := range cases {
		input := minimalInstallation(object{"kind": "linux", "host": "localhost"})
		for key, value := range override {
			input[key] = value
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		p, err := Production.Plan(ctx, encode(t, input))
		cancel()
		if err == nil || p.ID != "" {
			t.Fatalf("invalid configuration case %d returned a usable plan", i)
		}
	}
}
